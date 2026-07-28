// Package acpclient implements the small, safety-conscious ACP TCP client
// used to deliver review feedback to a retained agent session.
package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const protocolVersion = 1

// State describes an ACP connection/turn state suitable for a queue item.
type State string

const (
	StateConnecting State = "connecting"
	StateIdle       State = "idle"
	StateBusy       State = "busy"
	StateError      State = "error"
)

// Endpoint identifies a retained ACP session. ACP servers must remain
// loopback-only; remote agents are exposed through an SSH local forward.
type Endpoint struct {
	Host      string
	Port      int
	SessionID string
	CWD       string
}

// ValidateEndpoint validates an endpoint before any network connection.
func ValidateEndpoint(endpoint Endpoint) (Endpoint, error) {
	endpoint.Host = strings.TrimSpace(endpoint.Host)
	endpoint.SessionID = strings.TrimSpace(endpoint.SessionID)
	endpoint.CWD = strings.TrimSpace(endpoint.CWD)
	if !isLoopback(endpoint.Host) {
		return Endpoint{}, errors.New("ACP host must be localhost, 127.0.0.0/8, or ::1 (use an SSH local forward for remote agents)")
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return Endpoint{}, errors.New("ACP port must be an integer from 1 through 65535")
	}
	if endpoint.SessionID == "" || len(endpoint.SessionID) > 2048 {
		return Endpoint{}, errors.New("ACP session ID is required")
	}
	return endpoint, nil
}

func isLoopback(host string) bool {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if normalized == "localhost" || normalized == "::1" {
		return true
	}
	ip := net.ParseIP(normalized)
	return ip != nil && ip.IsLoopback()
}

// Options configures connection lifecycle reporting. Callbacks are invoked by
// the socket reader goroutine and therefore must not block.
type Options struct {
	ConnectTimeout  time.Duration
	RequestTimeout  time.Duration
	OnState         func(State, error)
	OnSessionUpdate func(json.RawMessage)
}

// PromptResult is the structured ACP prompt response. Raw preserves forward
// compatible fields while StopReason covers current Copilot ACP responses.
type PromptResult struct {
	StopReason string          `json:"stopReason"`
	Raw        json.RawMessage `json:"-"`
}

// Client is a single TCP connection attached to one retained ACP session.
// It never creates a session: reopening it uses session/load when supported.
type Client struct {
	endpoint Endpoint
	conn     net.Conn
	decoder  *json.Decoder
	encoder  *json.Encoder
	opts     Options

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	busy    bool
	closed  bool
	done    chan struct{}
	once    sync.Once
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) != 0 {
		return fmt.Sprintf("ACP RPC error %d: %s (%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("ACP RPC error %d: %s", e.Code, e.Message)
}

// Connect initializes a server and attaches this connection to endpoint's
// existing session if the server advertises loadSession.
func Connect(ctx context.Context, endpoint Endpoint, options Options) (*Client, error) {
	validated, err := ValidateEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if options.ConnectTimeout <= 0 {
		options.ConnectTimeout = 10 * time.Second
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	state(options, StateConnecting, nil)

	dialer := net.Dialer{Timeout: options.ConnectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(strings.Trim(validated.Host, "[]"), strconv.Itoa(validated.Port)))
	if err != nil {
		state(options, StateError, err)
		return nil, fmt.Errorf("connect ACP at %s:%d: %w", validated.Host, validated.Port, err)
	}
	client := &Client{endpoint: validated, conn: conn, decoder: json.NewDecoder(conn), encoder: json.NewEncoder(conn), opts: options, pending: map[int64]chan response{}, done: make(chan struct{})}
	go client.readLoop()

	initializeCtx, cancel := client.withTimeout(ctx)
	defer cancel()
	var initialized struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if err := client.call(initializeCtx, "initialize", map[string]any{"protocolVersion": protocolVersion, "clientCapabilities": map[string]any{"fs": map[string]any{}}}, &initialized); err != nil {
		client.Close()
		state(options, StateError, err)
		return nil, err
	}
	if initialized.AgentCapabilities.LoadSession {
		loadCtx, cancelLoad := client.withTimeout(ctx)
		err = client.call(loadCtx, "session/load", map[string]any{"sessionId": validated.SessionID, "cwd": validated.CWD, "mcpServers": []any{}}, nil)
		cancelLoad()
		if err != nil {
			client.Close()
			state(options, StateError, err)
			return nil, fmt.Errorf("load ACP session %q: %w", validated.SessionID, err)
		}
	}
	state(options, StateIdle, nil)
	return client, nil
}

func (c *Client) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, c.opts.RequestTimeout)
}

// Endpoint returns the immutable connection endpoint.
func (c *Client) Endpoint() Endpoint { return c.endpoint }

// Busy reports whether prompt delivery is currently in flight.
func (c *Client) Busy() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.busy }

// Prompt submits text to the retained session. It deliberately refuses a
// concurrent prompt so a queue cannot duplicate or scramble feedback.
func (c *Client) Prompt(ctx context.Context, text string) (PromptResult, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return PromptResult{}, errors.New("ACP connection is closed")
	}
	if c.busy {
		c.mu.Unlock()
		return PromptResult{}, errors.New("ACP session is already processing a prompt")
	}
	c.busy = true
	c.mu.Unlock()
	state(c.opts, StateBusy, nil)
	defer func() {
		c.mu.Lock()
		c.busy = false
		closed := c.closed
		c.mu.Unlock()
		if !closed {
			state(c.opts, StateIdle, nil)
		}
	}()

	promptCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	var raw json.RawMessage
	err := c.call(promptCtx, "session/prompt", map[string]any{"sessionId": c.endpoint.SessionID, "prompt": []map[string]string{{"type": "text", "text": text}}}, &raw)
	if err != nil {
		state(c.opts, StateError, err)
		return PromptResult{}, err
	}
	result := PromptResult{Raw: append(json.RawMessage(nil), raw...)}
	_ = json.Unmarshal(raw, &result)
	return result, nil
}

// Cancel asks the agent to cancel the current retained session turn.
func (c *Client) Cancel(ctx context.Context) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil
	}
	cancelCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	if err := c.call(cancelCtx, "session/cancel", map[string]any{"sessionId": c.endpoint.SessionID}, nil); err != nil {
		state(c.opts, StateError, err)
		return err
	}
	return nil
}

// Close releases the TCP connection and fails any outstanding RPC waits.
func (c *Client) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = map[int64]chan response{}
		c.mu.Unlock()
		close(c.done)
		_ = c.conn.Close()
		for _, ch := range pending {
			close(ch)
		}
	})
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("ACP connection is closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()

	c.writeMu.Lock()
	err := c.encoder.Encode(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	c.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("write ACP %s: %w", method, err)
	}
	select {
	case reply, ok := <-ch:
		if !ok {
			return errors.New("ACP connection closed")
		}
		if reply.Error != nil {
			return reply.Error
		}
		if out != nil && len(reply.Result) > 0 {
			if raw, ok := out.(*json.RawMessage); ok {
				*raw = append((*raw)[:0], reply.Result...)
				return nil
			}
			if err := json.Unmarshal(reply.Result, out); err != nil {
				return fmt.Errorf("decode ACP %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ACP %s: %w", method, ctx.Err())
	case <-c.done:
		return errors.New("ACP connection closed")
	}
}

func (c *Client) readLoop() {
	for {
		var message response
		if err := c.decoder.Decode(&message); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				state(c.opts, StateError, fmt.Errorf("read ACP response: %w", err))
			}
			c.Close()
			return
		}
		if message.Method != "" {
			c.handleInboundRequest(message)
			continue
		}
		id, err := parseID(message.ID)
		if err != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- message:
			default:
			}
		}
	}
}

func (c *Client) handleInboundRequest(message response) {
	if c.opts.OnSessionUpdate != nil && (message.Method == "session/update" || message.Method == "sessionUpdate") {
		c.opts.OnSessionUpdate(append(json.RawMessage(nil), message.Params...))
	}
	if len(message.ID) == 0 {
		return
	}
	// The feedback bridge never grants agent tool permissions.  This is
	// intentionally independent from any interactive agent approval UI.
	if message.Method == "session/request_permission" || message.Method == "session/requestPermission" {
		c.writeMu.Lock()
		_ = c.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID), "result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}})
		c.writeMu.Unlock()
		return
	}
	c.writeMu.Lock()
	_ = c.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID), "error": map[string]any{"code": -32601, "message": "method not supported by localreview feedback bridge"}})
	c.writeMu.Unlock()
}

func parseID(raw json.RawMessage) (int64, error) {
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, err
	}
	return id, nil
}

func state(options Options, next State, err error) {
	if options.OnState != nil {
		options.OnState(next, err)
	}
}
