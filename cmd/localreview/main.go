// localreview is the Go control-plane CLI. It intentionally talks to the
// loopback daemon capability instead of opening SQLite itself, so every CLI
// mutation follows the same authentication and audit boundary as Queue Home.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/daemon"
	copilotsetup "github.com/uhvesta/cmux-localreview/internal/setup"
	"github.com/uhvesta/cmux-localreview/internal/snapshot"
)

type discovery struct {
	Port      int    `json:"port"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	CreatedAt string `json:"createdAt"`
}

func dataDir() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CMUX_LOCALREVIEW_DATA_DIR")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "cmux-localreview"), nil
}
func runDaemon(args []string) error {
	flags := flag.NewFlagSet("daemon", flag.ContinueOnError)
	port := flags.Int("port", 0, "loopback port")
	data := flags.String("data-dir", "", "data directory")
	ui := flags.String("ui-dir", "", "built web application directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d, err := daemon.Start(ctx, daemon.Options{Port: *port, DataDir: *data, UIDir: *ui})
	if err != nil {
		return err
	}
	fmt.Printf("cmux-localreview Go daemon listening on 127.0.0.1:%d\n", d.Port())
	<-ctx.Done()
	return d.Close()
}

// daemonCommand is intentionally a process-management command, not another
// HTTP API. The discovery capability remains private to the daemon owner, and
// stop verifies the serving process identity before delivering SIGTERM.
// Keeping the historical `localreview daemon --port …` spelling working makes
// the Go cutover painless while `daemon run|status|stop` is the documented
// stable interface.
func daemonCommand(args []string) error {
	if len(args) == 0 {
		return runDaemon(nil)
	}
	switch args[0] {
	case "run":
		return runDaemon(args[1:])
	case "status":
		if len(args) != 1 {
			return errors.New("usage: localreview daemon status")
		}
		return daemonStatus(os.Stdout)
	case "stop":
		if len(args) != 1 {
			return errors.New("usage: localreview daemon stop")
		}
		return daemonStop()
	default:
		// Backwards-compatible flag-only invocation: `localreview daemon
		// --port 0` is equivalent to `localreview daemon run --port 0`.
		if strings.HasPrefix(args[0], "-") {
			return runDaemon(args)
		}
		return errors.New("usage: localreview daemon <run|status|stop> [options]")
	}
}

type daemonHealth struct {
	OK      bool   `json:"ok"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

func healthFor(d discovery) (daemonHealth, error) {
	if d.Port < 1 || d.Port > 65535 {
		return daemonHealth{}, errors.New("invalid localreviewd discovery record")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", d.Port))
	if err != nil {
		return daemonHealth{}, fmt.Errorf("localreviewd is not responding on 127.0.0.1:%d: %w", d.Port, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return daemonHealth{}, fmt.Errorf("localreviewd health check failed: %s", response.Status)
	}
	var health daemonHealth
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&health); err != nil {
		return daemonHealth{}, fmt.Errorf("decode localreviewd health response: %w", err)
	}
	if !health.OK || health.PID <= 0 {
		return daemonHealth{}, errors.New("localreviewd returned an invalid health response")
	}
	return health, nil
}

func daemonStatus(out io.Writer) error {
	d, err := discovered()
	if err != nil {
		return err
	}
	health, err := healthFor(d)
	if err != nil {
		return err
	}
	if d.PID > 0 && d.PID != health.PID {
		return fmt.Errorf("refusing stale discovery record: record PID %d does not match serving localreviewd PID %d", d.PID, health.PID)
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"running":   true,
		"pid":       health.PID,
		"port":      d.Port,
		"version":   health.Version,
		"startedAt": d.CreatedAt,
	})
}

func signalDaemon(pid int) error {
	if pid <= 0 {
		return errors.New("invalid localreviewd PID")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}

func daemonProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func daemonStop() error {
	d, err := discovered()
	if err != nil {
		return err
	}
	health, err := healthFor(d)
	if err != nil {
		return err
	}
	if d.PID <= 0 || d.PID != health.PID {
		return fmt.Errorf("refusing to stop: discovery PID %d does not match serving localreviewd PID %d", d.PID, health.PID)
	}
	if err := signalDaemon(health.PID); err != nil {
		return fmt.Errorf("stop localreviewd PID %d: %w", health.PID, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := healthFor(d); err != nil && !daemonProcessAlive(health.PID) {
			// The discovery record is trusted only after identity verification
			// above. Removing it here prevents a stale record from blocking a
			// subsequent daemon start, without ever deleting an arbitrary path.
			dir, dirErr := dataDir()
			if dirErr == nil {
				_ = os.Remove(filepath.Join(dir, "daemon.json"))
			}
			fmt.Printf("Stopped localreviewd PID %d.\n", health.PID)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("localreviewd PID %d did not stop within 5s", health.PID)
}
func discovered() (discovery, error) {
	dir, err := dataDir()
	if err != nil {
		return discovery{}, err
	}
	contents, err := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if err != nil {
		return discovery{}, errors.New("localreviewd is not running; start `localreview daemon`")
	}
	var value discovery
	if err := json.Unmarshal(contents, &value); err != nil || value.Port == 0 || value.Token == "" {
		return discovery{}, errors.New("invalid localreviewd discovery record")
	}
	return value, nil
}

// daemonExecutable resolves the native sidecar without assuming a developer
// checkout or a globally installed binary. Release archives put both binaries
// next to each other; PATH remains useful for contributor builds.
func daemonExecutable() (string, error) {
	self, selfErr := os.Executable()
	if selfErr == nil {
		name := "localreviewd"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(filepath.Dir(self), name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("localreviewd")
	if err != nil {
		return "", errors.New("localreviewd is not running and its native binary was not found beside localreview or on PATH")
	}
	return path, nil
}

// readyDaemon is the CLI's lifecycle boundary. Commands never touch SQLite
// directly: when the daemon is absent or stale, they start exactly one native
// loopback daemon and wait for its owner-only discovery record to become
// healthy. This gives release installs the promised `localreview open` / submit
// first-run behavior without reusing a process whose identity cannot be
// verified.
func readyDaemon() (discovery, error) {
	if current, err := discovered(); err == nil {
		if health, healthErr := healthFor(current); healthErr == nil && (current.PID <= 0 || health.PID == current.PID) {
			return current, nil
		}
	}
	dir, err := dataDir()
	if err != nil {
		return discovery{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return discovery{}, err
	}
	lockPath := filepath.Join(dir, "daemon-start.lock")
	lock, lockErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if lockErr != nil {
		if !errors.Is(lockErr, os.ErrExist) {
			return discovery{}, lockErr
		}
		// Another CLI invocation owns startup. Wait for its authenticated
		// discovery record instead of racing it with another daemon.
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if current, readErr := discovered(); readErr == nil {
				if health, healthErr := healthFor(current); healthErr == nil && (current.PID <= 0 || health.PID == current.PID) {
					return current, nil
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return discovery{}, errors.New("another localreview command is starting localreviewd but it did not become healthy within 8 seconds")
	}
	_ = lock.Close()
	defer os.Remove(lockPath)
	// A stale discovery record must never be treated as authority. It is safe to
	// replace only this exact daemon-owned record after its health check failed.
	_ = os.Remove(filepath.Join(dir, "daemon.json"))
	binary, err := daemonExecutable()
	if err != nil {
		return discovery{}, err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return discovery{}, err
	}
	command := exec.Command(binary, "--port", "0")
	command.Env = append(os.Environ(), "CMUX_LOCALREVIEW_DATA_DIR="+dir)
	command.Stdout = devNull
	command.Stderr = devNull
	if err := command.Start(); err != nil {
		_ = devNull.Close()
		return discovery{}, fmt.Errorf("start localreviewd: %w", err)
	}
	_ = devNull.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if current, readErr := discovered(); readErr == nil {
			if health, healthErr := healthFor(current); healthErr == nil && (current.PID <= 0 || health.PID == current.PID) {
				return current, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return discovery{}, errors.New("started localreviewd but it did not become healthy within 8 seconds; run `localreview daemon status` for diagnostics")
}

// daemonCall is the only HTTP boundary used by native CLI subcommands. The
// daemon capability is read from its owner-only discovery record and never
// printed or stored by the CLI.
func daemonCall(method, apiPath string, body any) (int, json.RawMessage, error) {
	d, err := readyDaemon()
	if err != nil {
		return 0, nil, err
	}
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d/api%s", d.Port, apiPath), input)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(contents))
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(contents, &payload)
		if payload.Error != "" {
			message = payload.Error
		}
		return response.StatusCode, nil, fmt.Errorf("daemon %s %s failed (%s): %s", method, apiPath, response.Status, message)
	}
	return response.StatusCode, json.RawMessage(contents), nil
}

func printDaemonJSON(method, path string, body any) error {
	_, response, err := daemonCall(method, path, body)
	if err != nil {
		return err
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, response, "", "  ") == nil {
		fmt.Println(formatted.String())
	} else {
		fmt.Println(string(response))
	}
	return nil
}

func configureAuthCapability(capability, clientID string) error {
	configure := map[string]string{"capability": capability, "clientId": clientID}
	_, _, err := daemonCall(http.MethodPost, "/github/auth/configure", configure)
	return err
}

func authCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: localreview auth <login|status|logout> [options]")
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("usage: localreview auth status")
		}
		return printDaemonJSON(http.MethodGet, "/github/auth/status", nil)
	case "logout":
		if len(args) != 2 {
			return errors.New("usage: localreview auth logout <read|write|copilot>")
		}
		return printDaemonJSON(http.MethodDelete, "/github/auth/"+args[1], nil)
	case "login":
		flags := flag.NewFlagSet("auth login", flag.ContinueOnError)
		capability := flags.String("capability", "copilot", "GitHub App capability: read, write, or copilot")
		clientID := flags.String("client-id", "", "public GitHub App client ID to configure before login")
		device := flags.Bool("device", false, "use device OAuth for a headless or SSH-only machine")
		loopback := flags.Bool("loopback", false, "use the default browser OAuth callback http://127.0.0.1:8787/oauth/callback")
		noWait := flags.Bool("no-wait", false, "print authorization instructions without polling")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: localreview auth login [--capability CAPABILITY] [--client-id ID] [--device|--loopback] [--no-wait]")
		}
		if *capability != "read" && *capability != "write" && *capability != "copilot" {
			return errors.New("capability must be read, write, or copilot")
		}
		if *device && *loopback {
			return errors.New("choose only one of --device or --loopback")
		}
		if *clientID != "" {
			if err := configureAuthCapability(*capability, *clientID); err != nil {
				return err
			}
		}
		flow := "loopback"
		if *device {
			flow = "device"
		}
		_, response, err := daemonCall(http.MethodPost, "/github/auth/"+*capability+"/start", map[string]string{"flow": flow})
		if err != nil {
			return err
		}
		var start struct {
			Flow             string `json:"flow"`
			AuthorizationURL string `json:"authorizationUrl"`
			UserCode         string `json:"userCode"`
			VerificationURI  string `json:"verificationUri"`
		}
		if err := json.Unmarshal(response, &start); err != nil {
			return err
		}
		if start.Flow == "loopback" {
			fmt.Printf("Open %s to authorize GitHub %s.\n", start.AuthorizationURL, *capability)
		} else {
			fmt.Printf("Open %s and enter code %s.\n", start.VerificationURI, start.UserCode)
		}
		if *noWait {
			return nil
		}
		for {
			time.Sleep(2 * time.Second)
			_, result, err := daemonCall(http.MethodPost, "/github/auth/"+*capability+"/poll", nil)
			if err != nil {
				return err
			}
			var status struct {
				Authenticated bool   `json:"authenticated"`
				State         string `json:"loginState"`
				Error         string `json:"error"`
			}
			if err := json.Unmarshal(result, &status); err != nil {
				return err
			}
			if status.Authenticated {
				fmt.Printf("GitHub %s capability connected.\n", *capability)
				return nil
			}
			if status.State == "error" || status.Error != "" {
				return fmt.Errorf("GitHub %s login failed: %s", *capability, status.Error)
			}
		}
	default:
		return errors.New("usage: localreview auth <login|status|logout> [options]")
	}
}

// githubAppCommand preserves the old setup-oriented vocabulary while routing
// every credential operation through the native auth surface. It never accepts
// a token or OAuth client secret: the public client uses PKCE.
func githubAppCommand(args []string) error {
	if len(args) == 0 || args[0] == "guide" {
		if len(args) > 1 {
			return errors.New("usage: localreview github-app guide")
		}
		fmt.Fprintln(os.Stdout, "Create dedicated GitHub OAuth Apps for cmux-localreview capabilities: read, write, and copilot.")
		fmt.Fprintln(os.Stdout, "Use minimum permissions for each capability. Configure each app with a loopback callback and device flow, then run:")
		fmt.Fprintln(os.Stdout, "  localreview github-app configure --capability copilot --client-id <client-id>")
		fmt.Fprintln(os.Stdout, "  localreview github-app connect --capability copilot  # browser loopback (default; register http://127.0.0.1:8787/oauth/callback)")
		fmt.Fprintln(os.Stdout, "  localreview github-app connect --capability copilot --device  # SSH/headless fallback")
		fmt.Fprintln(os.Stdout, "Access tokens stay in the OS secret store; they are never printed by this CLI. The browser flow uses PKCE and requires no client secret.")
		return nil
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("usage: localreview github-app status")
		}
		return authCommand([]string{"status"})
	case "disconnect":
		flags := flag.NewFlagSet("github-app disconnect", flag.ContinueOnError)
		capability := flags.String("capability", "", "GitHub App capability: read, write, or copilot")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *capability == "" {
			return errors.New("usage: localreview github-app disconnect --capability <read|write|copilot>")
		}
		return authCommand([]string{"logout", *capability})
	case "configure":
		flags := flag.NewFlagSet("github-app configure", flag.ContinueOnError)
		capability := flags.String("capability", "", "GitHub App capability: read, write, or copilot")
		clientID := flags.String("client-id", "", "public GitHub OAuth client ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *capability == "" || *clientID == "" {
			return errors.New("usage: localreview github-app configure --capability <read|write|copilot> --client-id <id>")
		}
		if err := configureAuthCapability(*capability, *clientID); err != nil {
			return err
		}
		fmt.Printf("Saved %s GitHub App client ID. Run `localreview github-app connect --capability %s`.\n", *capability, *capability)
		return nil
	case "connect":
		flags := flag.NewFlagSet("github-app connect", flag.ContinueOnError)
		capability := flags.String("capability", "", "GitHub App capability: read, write, or copilot")
		device := flags.Bool("device", false, "use device OAuth for a headless or SSH-only machine")
		loopback := flags.Bool("loopback", false, "use the default registered browser OAuth callback http://127.0.0.1:8787/oauth/callback")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *capability == "" {
			return errors.New("usage: localreview github-app connect --capability <read|write|copilot> [--device|--loopback]")
		}
		if *device && *loopback {
			return errors.New("choose only one of --device or --loopback")
		}
		loginArgs := []string{"login", "--capability", *capability}
		if *device {
			loginArgs = append(loginArgs, "--device")
		} else if *loopback {
			loginArgs = append(loginArgs, "--loopback")
		}
		return authCommand(loginArgs)
	default:
		return errors.New("usage: localreview github-app <guide|configure|connect|status|disconnect> [options]")
	}
}

func remoteCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: localreview remote <daemon|submit|status> [options]")
	}
	switch args[0] {
	case "daemon":
		// Run this on the remote host when it is acting as a loopback-only
		// worker. Federation transport is deliberately not hidden behind this
		// command: an SSH forward remains an explicit operator action.
		return daemonCommand(args[1:])
	case "status":
		if len(args) != 1 {
			return errors.New("usage: localreview remote status")
		}
		return printDaemonJSON(http.MethodGet, "/federation/nodes", nil)
	case "submit":
		flags := flag.NewFlagSet("remote submit", flag.ContinueOnError)
		workspace := flags.String("workspace", "", "local cache/worktree path recorded for this PR")
		title := flags.String("title", "", "review title")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: localreview remote submit [--workspace PATH] [--title TITLE] <PR URL>")
		}
		path := *workspace
		if path == "" {
			path, _ = os.Getwd()
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if *title == "" {
			*title = "Review " + flags.Arg(0)
		}
		return printDaemonJSON(http.MethodPost, "/queue", map[string]string{"title": *title, "workspacePath": absolute, "kind": "remote", "remoteUrl": flags.Arg(0)})
	default:
		return errors.New("usage: localreview remote <daemon|submit|status> [options]")
	}
}

func federationCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: localreview federation <add|list|connect|disconnect|delete|queue|workspaces> [options]")
	}
	switch args[0] {
	case "add":
		flags := flag.NewFlagSet("federation add", flag.ContinueOnError)
		id := flags.String("id", "", "stable node ID")
		label := flags.String("label", "", "display label")
		sshTarget := flags.String("ssh", "", "SSH target (user@host)")
		port := flags.Int("port", 0, "remote loopback daemon port")
		tokenStdin := flags.Bool("token-stdin", false, "read remote daemon discovery token from standard input")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *label == "" || *sshTarget == "" || *port == 0 || !*tokenStdin || flags.NArg() != 0 {
			return errors.New("usage: localreview federation add --id ID --label LABEL --ssh user@host --port PORT --token-stdin")
		}
		secret, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return fmt.Errorf("read remote daemon token: %w", err)
		}
		token := strings.TrimSpace(string(secret))
		if token == "" {
			return errors.New("remote daemon token from standard input is empty")
		}
		return printDaemonJSON(http.MethodPost, "/federation/nodes", map[string]any{"id": *id, "label": *label, "sshTarget": *sshTarget, "remotePort": *port, "token": token})
	case "list":
		if len(args) != 1 {
			return errors.New("usage: localreview federation list")
		}
		return printDaemonJSON(http.MethodGet, "/federation/nodes", nil)
	case "connect", "disconnect":
		if len(args) != 2 {
			return fmt.Errorf("usage: localreview federation %s <node-id>", args[0])
		}
		return printDaemonJSON(http.MethodPost, "/federation/nodes/"+url.PathEscape(args[1])+"/"+args[0], nil)
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: localreview federation delete <node-id>")
		}
		return printDaemonJSON(http.MethodDelete, "/federation/nodes/"+url.PathEscape(args[1]), nil)
	case "queue", "workspaces":
		flags := flag.NewFlagSet("federation "+args[0], flag.ContinueOnError)
		refresh := flags.Bool("refresh", false, "bypass 15-second cache")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() > 1 || (args[0] == "workspaces" && flags.NArg() == 0) {
			return fmt.Errorf("usage: localreview federation %s [node-id] [--refresh]", args[0])
		}
		path := "/federation/" + args[0]
		if flags.NArg() == 1 {
			path = "/federation/nodes/" + url.PathEscape(flags.Arg(0)) + "/" + args[0]
		}
		if *refresh {
			path += "?refresh=true"
		}
		return printDaemonJSON(http.MethodGet, path, nil)
	default:
		return errors.New("usage: localreview federation <add|list|connect|disconnect|delete|queue|workspaces> [options]")
	}
}
func submit(args []string) error {
	flags := flag.NewFlagSet("queue-submit", flag.ContinueOnError)
	title := flags.String("title", "", "queue title")
	topic := flags.String("topic", "", "stable review topic")
	kind := flags.String("kind", "local", "local or remote")
	remoteURL := flags.String("remote-url", "", "remote pull request URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: localreview queue-submit [--title TITLE] [--topic TOPIC] <workspace-path>")
	}
	workspace, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return err
	}
	if *title == "" {
		*title = "Review " + workspace
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	_, manifestPath, err := snapshot.Capture(workspace, filepath.Join(dir, "artifacts"), "")
	if err != nil {
		return err
	}
	d, err := readyDaemon()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"title": *title, "workspacePath": workspace, "reviewTopic": *topic, "kind": *kind, "remoteUrl": *remoteURL, "snapshotManifestPath": manifestPath})
	request, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/queue", d.Port), bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+d.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return fmt.Errorf("queue submission failed: %s", response.Status)
	}
	var result map[string]any
	_ = json.NewDecoder(response.Body).Decode(&result)
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
	return nil
}
func reproduce(args []string) error {
	flags := flag.NewFlagSet("reproduce", flag.ContinueOnError)
	copilotSetup := flags.Bool("copilot", false, "reproduce a queued snapshot and print SDK-native /ask continuation guidance")
	jsonOutput := flags.Bool("json", false, "print machine-readable reproduction details")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("usage: localreview reproduce [--copilot] [--json] <manifest.json-or-queue-id> <empty-destination>")
	}
	destination, err := filepath.Abs(flags.Arg(1))
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(destination); err == nil && len(entries) > 0 {
		return errors.New("destination must be empty")
	}
	manifestPath := flags.Arg(0)
	var queueID string
	var copilotSessionID *string
	if *copilotSetup {
		queueID = flags.Arg(0)
		_, response, err := daemonCall(http.MethodGet, "/queue/"+queueID, nil)
		if err != nil {
			return err
		}
		var detail struct {
			Item struct {
				SnapshotManifestPath *string `json:"snapshotManifestPath"`
				CopilotSessionID     *string `json:"copilotSessionId"`
			} `json:"item"`
		}
		if err := json.Unmarshal(response, &detail); err != nil {
			return fmt.Errorf("decode queue reproduction metadata: %w", err)
		}
		if detail.Item.SnapshotManifestPath == nil || strings.TrimSpace(*detail.Item.SnapshotManifestPath) == "" {
			return fmt.Errorf("queue item %s has no retained immutable snapshot", queueID)
		}
		manifestPath = *detail.Item.SnapshotManifestPath
		copilotSessionID = detail.Item.CopilotSessionID
	}
	manifest, err := snapshot.Materialize(manifestPath, destination)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"snapshotId":       manifest.ID,
			"cwd":              destination,
			"queueItemId":      queueID,
			"copilotSessionId": copilotSessionID,
			"openReviewer":     "localreview open " + shellQuoteCLI(destination),
			"notes": []string{
				"Open the reproduced workspace to start a fresh, SDK-native /ask conversation.",
				"Historic transcripts remain readable but are never replayed or resumed automatically.",
			},
		})
	}
	fmt.Printf("Reproduced snapshot %s\ncwd: %s\n", manifest.ID, destination)
	if *copilotSetup {
		fmt.Printf("Queue item: %s\n", queueID)
		if copilotSessionID != nil && strings.TrimSpace(*copilotSessionID) != "" {
			fmt.Printf("Historic Copilot session ID: %s (reference only; it is not resumed).\n", *copilotSessionID)
		}
		fmt.Printf("Start a fresh SDK-native /ask conversation: localreview open %s\n", shellQuoteCLI(destination))
	}
	return nil
}

func shellQuoteCLI(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// setupCopilot installs only cmux-localreview-managed Copilot instructions and
// skills. Existing user-owned files are preserved, and --dry-run has no write
// side effects so it is safe for remote/bootstrap automation.
func setupCopilot(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	personal := flags.Bool("personal", false, "also install skills in ~/.copilot/skills")
	noProject := flags.Bool("no-project", false, "do not install project-local .github skills")
	dryRun := flags.Bool("dry-run", false, "show planned changes without writing")
	jsonOutput := flags.Bool("json", false, "print machine-readable changes")
	command := flags.String("command", "", "submission command written into installed skills")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: localreview setup [--personal] [--no-project] [--dry-run] [workspace]")
	}
	workspace := "."
	if flags.NArg() == 1 {
		workspace = flags.Arg(0)
	}
	changes, err := copilotsetup.Install(copilotsetup.Options{Workspace: workspace, Project: !*noProject, Personal: *personal, DryRun: *dryRun, Command: *command})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"workspace": workspace, "dryRun": *dryRun, "changes": changes})
	}
	for _, change := range changes {
		if change.Reason == "" {
			fmt.Printf("%-9s %s\n", change.Action, change.Path)
		} else {
			fmt.Printf("%-9s %s — %s\n", change.Action, change.Path, change.Reason)
		}
	}
	return nil
}

// openHome creates the one-time browser capability URL. The daemon token is
// deliberately kept in the URL fragment: browsers never transmit fragments
// to the HTTP server, and the React client exchanges it immediately for an
// HttpOnly, same-origin cookie.
func openHome(args []string) error {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	noOpen := flags.Bool("no-open", false, "print the Queue Home or workspace-review URL without launching a browser")
	queueItem := flags.String("queue-item", "", "open one persisted queue item directly in the reviewer")
	pullRequest := flags.String("pr", "", "open a GitHub pull request for local read-only review and /ask (never queues or publishes it)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 || (*queueItem != "" && flags.NArg() != 0) || (*pullRequest != "" && (flags.NArg() != 0 || *queueItem != "")) {
		return errors.New("usage: localreview open [--no-open] [--queue-item ID | --pr URL] [workspace-path]")
	}
	d, err := readyDaemon()
	if err != nil {
		return err
	}
	path := "/"
	if strings.TrimSpace(*pullRequest) != "" {
		value := strings.TrimSpace(*pullRequest)
		parsed, parseErr := url.Parse(value)
		if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || !strings.Contains(parsed.Path, "/pull/") {
			return errors.New("--pr must be a GitHub pull-request URL, for example https://github.com/owner/repo/pull/123")
		}
		_, body, callErr := daemonCall(http.MethodPost, "/local-review/pr", map[string]string{"remoteUrl": value})
		if callErr != nil {
			return fmt.Errorf("open local PR review: %w", callErr)
		}
		var opened struct {
			ReviewURL string `json:"reviewUrl"`
		}
		if err := json.Unmarshal(body, &opened); err != nil || opened.ReviewURL == "" {
			return errors.New("localreviewd returned an invalid local PR review response")
		}
		path = opened.ReviewURL
		if !strings.Contains(path, "localOnly=1") {
			return errors.New("localreviewd refused to open a local-only PR review")
		}
	} else if strings.TrimSpace(*queueItem) != "" {
		path = "/review?queueItem=" + url.QueryEscape(strings.TrimSpace(*queueItem))
	}
	if flags.NArg() == 1 {
		workspace, err := filepath.Abs(flags.Arg(0))
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"workspacePath": workspace})
		request, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/workspaces/open", d.Port), bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+d.Token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("workspace activation failed: %s", response.Status)
		}
		path = "/review"
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s#daemonToken=%s", d.Port, path, d.Token)
	fmt.Println(url)
	if *noOpen {
		return nil
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("Queue Home URL printed above, but could not launch a browser: %w", err)
	}
	return nil
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "usage: localreview <daemon|open|submit|queue-submit|reproduce|setup|auth|github-app|remote|federation|demo> [options]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Run `localreview <command> --help` for command flags.")
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	if os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help" {
		usage(os.Stdout)
		return
	}
	var err error
	switch os.Args[1] {
	case "daemon":
		err = daemonCommand(os.Args[2:])
	case "submit", "queue-submit":
		err = submit(os.Args[2:])
	case "reproduce":
		err = reproduce(os.Args[2:])
	case "setup":
		err = setupCopilot(os.Args[2:])
	case "auth":
		err = authCommand(os.Args[2:])
	case "github-app":
		err = githubAppCommand(os.Args[2:])
	case "remote":
		err = remoteCommand(os.Args[2:])
	case "federation":
		err = federationCommand(os.Args[2:])
	case "open":
		err = openHome(os.Args[2:])
	case "demo":
		// The native demo is intentionally just Queue Home: unlike the retired
		// TypeScript demo it never starts a second server or fabricates review
		// state. Pass a workspace path to activate it before opening.
		err = openHome(os.Args[2:])
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	fmt.Fprintln(os.Stderr, "localreview:", err)
	os.Exit(1)
}
