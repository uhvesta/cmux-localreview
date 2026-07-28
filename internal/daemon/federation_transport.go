package daemon

// The federation transport deliberately uses OpenSSH as an external, audited
// transport rather than implementing SSH in the daemon. Every HTTP request is
// made to an ephemeral 127.0.0.1 listener created with `ssh -L`; the remote
// daemon continues to bind only loopback and its bearer token never reaches a
// browser. The interfaces below make that process hermetic in tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/federation"
)

const federationCacheTTL = 15 * time.Second

// FederationTunnel is one live, loopback-only path to a remote daemon.
// Close is idempotent and ends the forwarding process where applicable.
type FederationTunnel interface {
	Endpoint() federation.TunnelEndpoint
	Close() error
}

// FederationDialer establishes a tunnel. The token is intentionally not an
// argument: it authenticates HTTP after forwarding, not SSH itself.
type FederationDialer interface {
	Dial(context.Context, federation.Node) (FederationTunnel, error)
}

type sshFederationDialer struct{}

type sshFederationTunnel struct {
	endpoint federation.TunnelEndpoint
	command  *exec.Cmd
	done     <-chan error
	once     sync.Once
}

func (t *sshFederationTunnel) Endpoint() federation.TunnelEndpoint { return t.endpoint }
func (t *sshFederationTunnel) Close() error {
	var result error
	t.once.Do(func() {
		if t.command.Process != nil {
			_ = t.command.Process.Kill()
		}
		result = <-t.done
		if result != nil && !strings.Contains(result.Error(), "signal: killed") {
			return
		}
		result = nil
	})
	return result
}

func unusedLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForLoopback(ctx context.Context, endpoint federation.TunnelEndpoint, exited <-chan error, stderr *bytes.Buffer) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("SSH tunnel did not open %s:%d", endpoint.Host, endpoint.Port)
		case <-tick.C:
			select {
			case err := <-exited:
				detail := strings.TrimSpace(stderr.String())
				if detail != "" {
					return fmt.Errorf("SSH tunnel exited before opening: %s", detail)
				}
				if err != nil {
					return fmt.Errorf("SSH tunnel exited before opening: %w", err)
				}
				return errors.New("SSH tunnel exited before opening")
			default:
			}
		}
	}
}

// federationSSHConfigArgs permits a self-hosted deployment to select an
// explicit OpenSSH config without changing a user's global ~/.ssh/config.
// This is particularly useful for a service account, nonstandard SSH port, or
// a bastion Host alias. The config is an operator-owned transport input, never
// browser input; reject group/world-writable files so a local unrelated user
// cannot alter the daemon's SSH routing through an inherited environment.
func federationSSHConfigArgs() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("CMUX_LOCALREVIEW_SSH_CONFIG"))
	if raw == "" {
		return nil, nil
	}
	path, err := filepath.Abs(raw)
	if err != nil {
		return nil, fmt.Errorf("resolve CMUX_LOCALREVIEW_SSH_CONFIG: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read CMUX_LOCALREVIEW_SSH_CONFIG: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("CMUX_LOCALREVIEW_SSH_CONFIG must name a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("CMUX_LOCALREVIEW_SSH_CONFIG must not be group- or world-writable")
	}
	return []string{"-F", path}, nil
}

func (sshFederationDialer) Dial(ctx context.Context, node federation.Node) (FederationTunnel, error) {
	port, err := unusedLoopbackPort()
	if err != nil {
		return nil, fmt.Errorf("allocate loopback forwarding port: %w", err)
	}
	endpoint, err := federation.ParseLoopbackEndpoint("127.0.0.1", port)
	if err != nil {
		return nil, err
	}
	// No shell interpolation: target and forward specification are individual
	// argv values. BatchMode prevents any hidden password prompt from hanging a
	// browser request, and ExitOnForwardFailure rejects a half-open tunnel.
	// Do not set ClearAllForwardings=yes: OpenSSH applies it after parsing -L,
	// which silently removes the very loopback forward we are establishing.
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", endpoint.Port, node.RemotePort)
	configArgs, err := federationSSHConfigArgs()
	if err != nil {
		return nil, err
	}
	args := []string{"-N", "-T", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes"}
	args = append(args, configArgs...)
	args = append(args, "-L", forward, node.SSHTarget)
	// A tunnel remains live across several browser/API requests. Do not bind
	// the SSH process itself to this request context: net/http cancels it when
	// a successful Connect response is written, which would immediately tear
	// down a just-created forward. waitForLoopback still observes ctx while
	// startup is in progress, and federationTransport.Close/disconnect owns the
	// live process lifecycle afterward.
	cmd := exec.Command("ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start SSH tunnel: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := waitForLoopback(ctx, endpoint, done, &stderr); err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		default:
			<-done
		}
		return nil, err
	}
	return &sshFederationTunnel{endpoint: endpoint, command: cmd, done: done}, nil
}

type federationCachedResponse struct {
	queue        []map[string]any
	workspaces   []map[string]any
	queueAt      time.Time
	workspacesAt time.Time
}
type federationLiveConnection struct{ tunnel FederationTunnel }

type federationTransport struct {
	dialer FederationDialer
	client *http.Client
	mu     sync.Mutex
	live   map[string]federationLiveConnection
	cache  map[string]federationCachedResponse
}

func newFederationTransport(dialer FederationDialer) *federationTransport {
	if dialer == nil {
		dialer = sshFederationDialer{}
	}
	return &federationTransport{dialer: dialer, client: &http.Client{Timeout: 12 * time.Second}, live: map[string]federationLiveConnection{}, cache: map[string]federationCachedResponse{}}
}

func (t *federationTransport) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, item := range t.live {
		_ = item.tunnel.Close()
		delete(t.live, id)
	}
}
func (t *federationTransport) disconnect(id string) {
	t.mu.Lock()
	item, ok := t.live[id]
	delete(t.live, id)
	delete(t.cache, id)
	t.mu.Unlock()
	if ok {
		_ = item.tunnel.Close()
	}
}
func (t *federationTransport) runtime(id string, node federation.Node) federationRuntime {
	t.mu.Lock()
	live, alive := t.live[id]
	cached, cachedOK := t.cache[id]
	t.mu.Unlock()
	r := federationRuntime{ID: id, State: "disconnected", Available: true, CachedResponses: 0, LastConnectedAt: node.LastConnectedAt, LastError: node.LastError, Message: "SSH loopback federation available"}
	if !node.Enabled {
		r.State = "disabled"
		return r
	}
	if node.LastError != nil && strings.TrimSpace(*node.LastError) != "" {
		r.State = "error"
	}
	if alive {
		p := live.tunnel.Endpoint().Port
		r.LocalPort = &p
		r.State = "connected"
	}
	if cachedOK {
		r.CachedResponses = 1
		atTime := cached.queueAt
		if cached.workspacesAt.After(atTime) {
			atTime = cached.workspacesAt
		}
		if !atTime.IsZero() {
			at := atTime.UnixMilli()
			r.LastConnectedAt = &at
		}
	}
	return r
}

func (t *federationTransport) open(ctx context.Context, node federation.Node) (FederationTunnel, error) {
	t.mu.Lock()
	live, ok := t.live[node.ID]
	t.mu.Unlock()
	if ok {
		return live.tunnel, nil
	}
	tunnel, err := t.dialer.Dial(ctx, node)
	if err != nil {
		return nil, err
	}
	endpoint := tunnel.Endpoint()
	if _, err := federation.ParseLoopbackEndpoint(endpoint.Host, endpoint.Port); err != nil {
		_ = tunnel.Close()
		return nil, err
	}
	t.mu.Lock()
	if old, exists := t.live[node.ID]; exists {
		t.mu.Unlock()
		_ = tunnel.Close()
		return old.tunnel, nil
	}
	t.live[node.ID] = federationLiveConnection{tunnel: tunnel}
	t.mu.Unlock()
	return tunnel, nil
}

func decodeRemoteList(body []byte, key string) ([]map[string]any, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var result []map[string]any
	if raw, ok := envelope[key]; ok {
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode remote %s: %w", key, err)
		}
		return result, nil
	}
	return nil, fmt.Errorf("remote response omitted %q", key)
}

func (t *federationTransport) fetch(ctx context.Context, node federation.Node, token, resource string, refresh bool) ([]map[string]any, bool, error) {
	t.mu.Lock()
	cached, ok := t.cache[node.ID]
	at := cached.queueAt
	if resource == "workspaces" {
		at = cached.workspacesAt
	}
	fresh := ok && !at.IsZero() && time.Since(at) < federationCacheTTL
	var existing []map[string]any
	if resource == "queue" {
		existing = cached.queue
	} else {
		existing = cached.workspaces
	}
	t.mu.Unlock()
	if fresh && !refresh {
		return existing, true, nil
	}
	tunnel, err := t.open(ctx, node)
	if err != nil {
		return nil, false, err
	}
	e := tunnel.Endpoint()
	url := "http://" + net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) + "/api/" + resource
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.client.Do(req)
	if err != nil {
		t.disconnect(node.ID)
		return nil, false, fmt.Errorf("request remote %s: %w", resource, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.disconnect(node.ID)
		return nil, false, fmt.Errorf("remote %s returned %s", resource, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, fmt.Errorf("read remote %s: %w", resource, err)
	}
	items, err := decodeRemoteList(body, "items")
	if resource == "workspaces" {
		items, err = decodeRemoteList(body, "workspaces")
	}
	if err != nil {
		return nil, false, err
	}
	t.mu.Lock()
	updated := t.cache[node.ID]
	if resource == "queue" {
		updated.queue = items
	} else {
		updated.workspaces = items
	}
	if resource == "queue" {
		updated.queueAt = time.Now()
	} else {
		updated.workspacesAt = time.Now()
	}
	t.cache[node.ID] = updated
	t.mu.Unlock()
	return items, false, nil
}

func (t *federationTransport) queue(ctx context.Context, node federation.Node, token string, refresh bool) ([]map[string]any, bool, error) {
	return t.fetch(ctx, node, token, "queue", refresh)
}
func (t *federationTransport) workspaces(ctx context.Context, node federation.Node, token string, refresh bool) ([]map[string]any, bool, error) {
	return t.fetch(ctx, node, token, "workspaces", refresh)
}
