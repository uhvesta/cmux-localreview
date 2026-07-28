// Package federation owns the durable configuration and read model for remote
// localreviewd nodes. It deliberately does not create SSH processes: callers
// provide a tunnel/client implementation, while this package makes the
// persisted node lifecycle and aggregate Queue Home data deterministic.
package federation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// ConnectionState is the UI-safe lifecycle of a remote daemon connection.
// Connecting is deliberately transient; it is returned by a caller while a
// tunnel is being established and is never written into the secret-bearing
// node record.
type ConnectionState string

const (
	Disconnected ConnectionState = "disconnected"
	Connecting   ConnectionState = "connecting"
	Connected    ConnectionState = "connected"
	Error        ConnectionState = "error"
)

// Config is the secret-bearing input for a node. Token is never included in
// a read model or JSON response; it exists solely to authenticate daemon-to-
// daemon requests after the transport has connected.
type Config struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	SSHTarget  string `json:"sshTarget"`
	RemotePort int    `json:"remotePort"`
	Token      string `json:"-"`
	Enabled    bool   `json:"enabled"`
}

// TunnelEndpoint documents the only endpoint a remote client may use. The
// transport must bind it locally (normally via SSH -L) before issuing a
// request, so the daemon itself is never exposed beyond loopback.
type TunnelEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Node is the redacted, durable node state suitable for Queue Home.
type Node struct {
	ID              string          `json:"id"`
	Label           string          `json:"label"`
	SSHTarget       string          `json:"sshTarget"`
	RemotePort      int             `json:"remotePort"`
	Enabled         bool            `json:"enabled"`
	State           ConnectionState `json:"state"`
	Tunnel          TunnelEndpoint  `json:"tunnel"`
	LastConnectedAt *int64          `json:"lastConnectedAt"`
	LastError       *string         `json:"lastError"`
	CreatedAt       int64           `json:"createdAt"`
	UpdatedAt       int64           `json:"updatedAt"`
}

// RemoteQueueItem is the normalized, non-authoritative copy shown alongside
// local queue items. NodeID and NodeLabel make an aggregate item traceable
// without leaking its daemon capability.
type RemoteQueueItem struct {
	NodeID    string         `json:"nodeId"`
	NodeLabel string         `json:"nodeLabel"`
	Item      map[string]any `json:"item"`
}

// QueueClient is the sole transport boundary. A production implementation can
// use an SSH tunnel, while tests can be a static in-memory client.
type QueueClient interface {
	ListQueue(context.Context, Node) ([]map[string]any, error)
}

type Aggregate struct {
	Nodes []Node            `json:"nodes"`
	Items []RemoteQueueItem `json:"items"`
}

func validate(c Config) error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Label) == "" || strings.TrimSpace(c.SSHTarget) == "" {
		return errors.New("id, label, and sshTarget are required")
	}
	if strings.ContainsAny(c.SSHTarget, " \t\r\n/@") && strings.Count(c.SSHTarget, "@") > 1 {
		return errors.New("sshTarget must be a single host or user@host")
	}
	if strings.ContainsAny(c.SSHTarget, " \t\r\n/") {
		return errors.New("sshTarget must be a host or user@host, not a URL")
	}
	if c.RemotePort < 1 || c.RemotePort > 65535 {
		return errors.New("remotePort must be between 1 and 65535")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("daemon token is required")
	}
	return nil
}

// Upsert validates and persists a node. Secrets stay in SQLite only; callers
// receive the redacted Node view.
func Upsert(db *sql.DB, c Config) (*Node, error) {
	if err := validate(c); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	_, err := db.Exec(`INSERT INTO federation_nodes(id,label,ssh_target,remote_port,token,enabled,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET label=excluded.label,ssh_target=excluded.ssh_target,remote_port=excluded.remote_port,token=excluded.token,enabled=excluded.enabled,updated_at=excluded.updated_at`,
		c.ID, strings.TrimSpace(c.Label), strings.TrimSpace(c.SSHTarget), c.RemotePort, c.Token, c.Enabled, now, now)
	if err != nil {
		return nil, err
	}
	return Get(db, c.ID)
}

func state(enabled bool, connectedAt *int64, lastError *string) ConnectionState {
	if !enabled {
		return Disconnected
	}
	if lastError != nil && strings.TrimSpace(*lastError) != "" {
		return Error
	}
	if connectedAt != nil {
		return Connected
	}
	return Disconnected
}

func scan(row interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var enabled int
	if err := row.Scan(&n.ID, &n.Label, &n.SSHTarget, &n.RemotePort, &enabled, &n.LastConnectedAt, &n.LastError, &n.CreatedAt, &n.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	n.Enabled = enabled != 0
	n.State = state(n.Enabled, n.LastConnectedAt, n.LastError)
	n.Tunnel = TunnelEndpoint{Host: "127.0.0.1", Port: n.RemotePort}
	return &n, nil
}

func Get(db *sql.DB, id string) (*Node, error) {
	return scan(db.QueryRow(`SELECT id,label,ssh_target,remote_port,enabled,last_connected_at,last_error,created_at,updated_at FROM federation_nodes WHERE id=?`, id))
}

func List(db *sql.DB) ([]Node, error) {
	rows, err := db.Query(`SELECT id,label,ssh_target,remote_port,enabled,last_connected_at,last_error,created_at,updated_at FROM federation_nodes ORDER BY label,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// Remove deletes the complete secret-bearing configuration.  Callers must
// stop any live transport before invoking this operation; this package never
// owns a transport process itself.
func Remove(db *sql.DB, id string) (bool, error) {
	result, err := db.Exec(`DELETE FROM federation_nodes WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

// MarkConnected clears an old transport error only after a successful remote
// request, making retry state visible and preventing stale error banners.
func MarkConnected(db *sql.DB, id string) error {
	result, err := db.Exec(`UPDATE federation_nodes SET last_connected_at=?,last_error=NULL,updated_at=? WHERE id=?`, time.Now().UnixMilli(), time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return fmt.Errorf("node %q: %w", id, sql.ErrNoRows)
	}
	return nil
}

// MarkRetryableFailure retains the node configuration so the UI can offer a
// retry; it does not silently disable a configured remote node.
func MarkRetryableFailure(db *sql.DB, id string, cause error) error {
	if cause == nil {
		return errors.New("retryable failure requires an error")
	}
	result, err := db.Exec(`UPDATE federation_nodes SET last_error=?,updated_at=? WHERE id=?`, cause.Error(), time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return fmt.Errorf("node %q: %w", id, sql.ErrNoRows)
	}
	return nil
}

// Disconnect is an explicit user action. It disables future client calls but
// keeps the node configuration for a later reconnect/retry.
func Disconnect(db *sql.DB, id string) error {
	result, err := db.Exec(`UPDATE federation_nodes SET enabled=0,updated_at=? WHERE id=?`, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return fmt.Errorf("node %q: %w", id, sql.ErrNoRows)
	}
	return nil
}

// Reconnect re-enables a node after an explicit disconnect or a retryable
// failure. The returned state is Connecting so a caller can render immediate
// progress before its tunnel/client has produced a result. A successful
// request must subsequently call MarkConnected; a failed request must call
// MarkRetryableFailure.
func Reconnect(db *sql.DB, id string) (*Node, error) {
	now := time.Now().UnixMilli()
	result, err := db.Exec(`UPDATE federation_nodes SET enabled=1,last_error=NULL,updated_at=? WHERE id=?`, now, id)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return nil, fmt.Errorf("node %q: %w", id, sql.ErrNoRows)
	}
	n, err := Get(db, id)
	if err != nil {
		return nil, err
	}
	n.State = Connecting
	return n, nil
}

// AggregateQueue reads every enabled remote node through the supplied
// boundary. A failed node remains in the response with error state; healthy
// nodes continue to populate Queue Home instead of failing the whole page.
func AggregateQueue(ctx context.Context, db *sql.DB, client QueueClient) (*Aggregate, error) {
	if client == nil {
		return nil, errors.New("remote queue client is required")
	}
	nodes, err := List(db)
	if err != nil {
		return nil, err
	}
	result := &Aggregate{Nodes: nodes, Items: []RemoteQueueItem{}}
	for i := range result.Nodes {
		node := &result.Nodes[i]
		if !node.Enabled {
			continue
		}
		items, err := client.ListQueue(ctx, *node)
		if err != nil {
			if markErr := MarkRetryableFailure(db, node.ID, err); markErr != nil {
				return nil, markErr
			}
			node.State = Error
			message := err.Error()
			node.LastError = &message
			continue
		}
		if err := MarkConnected(db, node.ID); err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		node.State, node.LastError, node.LastConnectedAt = Connected, nil, &now
		for _, item := range items {
			result.Items = append(result.Items, RemoteQueueItem{NodeID: node.ID, NodeLabel: node.Label, Item: item})
		}
	}
	return result, nil
}

// ParseLoopbackEndpoint is a defensive helper for transport implementations.
// It rejects externally reachable tunnel destinations.
func ParseLoopbackEndpoint(host string, port int) (TunnelEndpoint, error) {
	if port < 1 || port > 65535 {
		return TunnelEndpoint{}, errors.New("port must be between 1 and 65535")
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return TunnelEndpoint{}, errors.New("tunnel endpoint must be a loopback IP")
	}
	return TunnelEndpoint{Host: parsed.String(), Port: port}, nil
}
