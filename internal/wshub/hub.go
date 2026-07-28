// Package wshub provides localreview's in-process reviewer update fanout.
// It is intentionally independent of daemon routes: mounting Hub.ServeHTTP
// at /ws is the sole integration point.
package wshub

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// DiffUpdated is the exact JSON shape consumed by the reviewer client after a
// changed Git source invalidates a repository diff cache.
type DiffUpdated struct {
	Type   string `json:"type"`
	RepoID string `json:"repoId"`
}

func NewDiffUpdated(repoID string) DiffUpdated {
	return DiffUpdated{Type: "diff-updated", RepoID: repoID}
}

type Options struct {
	Path                   string
	OnLastClientDisconnect func()
	WriteTimeout           time.Duration
}

type client struct {
	conn *websocket.Conn
	done chan struct{}
	once sync.Once
}

func (client *client) close() { client.once.Do(func() { close(client.done); client.conn.CloseNow() }) }

// Hub owns clients connected to one browser reviewer. It is safe to broadcast
// from filesystem watchers or request goroutines concurrently.
type Hub struct {
	path         string
	writeTimeout time.Duration
	onLast       func()
	mu           sync.Mutex
	clients      map[*client]struct{}
	closed       bool
}

func New(options Options) *Hub {
	path := options.Path
	if path == "" {
		path = "/ws"
	}
	timeout := options.WriteTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Hub{path: path, writeTimeout: timeout, onLast: options.OnLastClientDisconnect, clients: map[*client]struct{}{}}
}

func (hub *Hub) Path() string     { return hub.path }
func (hub *Hub) ClientCount() int { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.clients) }

func (hub *Hub) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != hub.path {
		http.NotFound(writer, request)
		return
	}
	hub.mu.Lock()
	closed := hub.closed
	hub.mu.Unlock()
	if closed {
		http.Error(writer, "websocket hub is closed", http.StatusServiceUnavailable)
		return
	}
	// coder/websocket's default same-origin verification protects this
	// loopback daemon from a different local web page subscribing to review
	// updates. The authenticated daemon may opt into an explicit origin policy
	// when it mounts the hub, but never disables verification by default.
	conn, err := websocket.Accept(writer, request, nil)
	if err != nil {
		return
	}
	client := &client{conn: conn, done: make(chan struct{})}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		client.close()
		return
	}
	hub.clients[client] = struct{}{}
	hub.mu.Unlock()
	go hub.readClient(client)
}

func (hub *Hub) readClient(client *client) {
	// The browser sends no application messages today; reads exist solely to
	// observe clean disconnects and release the connection deterministically.
	for {
		_, _, err := client.conn.Reader(context.Background())
		if err != nil {
			hub.remove(client)
			return
		}
	}
}

// Broadcast serializes once and makes a bounded write attempt to every live
// client. A slow or dead browser is removed rather than stalling diff updates.
func (hub *Hub) Broadcast(message any) {
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	hub.mu.Lock()
	clients := make([]*client, 0, len(hub.clients))
	for client := range hub.clients {
		clients = append(clients, client)
	}
	hub.mu.Unlock()
	for _, client := range clients {
		go hub.write(client, payload)
	}
}

func (hub *Hub) BroadcastDiffUpdated(repoID string) { hub.Broadcast(NewDiffUpdated(repoID)) }

func (hub *Hub) write(client *client, payload []byte) {
	select {
	case <-client.done:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), hub.writeTimeout)
	defer cancel()
	if err := client.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		hub.remove(client)
	}
}

func (hub *Hub) remove(client *client) {
	client.close()
	hub.mu.Lock()
	_, exists := hub.clients[client]
	if exists {
		delete(hub.clients, client)
	}
	last := exists && len(hub.clients) == 0
	callback := hub.onLast
	hub.mu.Unlock()
	if last && callback != nil {
		callback()
	}
}

func (hub *Hub) Close() {
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return
	}
	hub.closed = true
	clients := make([]*client, 0, len(hub.clients))
	for client := range hub.clients {
		clients = append(clients, client)
	}
	hub.mu.Unlock()
	for _, client := range clients {
		hub.remove(client)
	}
}
