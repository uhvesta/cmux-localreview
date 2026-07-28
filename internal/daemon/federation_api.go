package daemon

// Native SSH federation API. Node credentials remain daemon-only; browser
// responses receive only redacted metadata and runtime tunnel state.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/federation"
)

const federationSecretService = "cmux-localreview.federation"

func federationSecretAccount(id string) string { return "remote-daemon:" + id }
func (d *Daemon) federationToken(id string) (string, error) {
	if d.federationSecrets == nil {
		return "", fmt.Errorf("system secret store is unavailable")
	}
	token, err := d.federationSecrets.Get(federationSecretService, federationSecretAccount(id))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("remote daemon capability is missing; re-add this node")
	}
	return token, nil
}

type federationNodeInput struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	SSHTarget  string `json:"sshTarget"`
	RemotePort int    `json:"remotePort"`
	Token      string `json:"token"`
	Enabled    *bool  `json:"enabled"`
}

// federationRuntime is intentionally separate from federation.Node.State.
// Node.State is durable historical metadata (for example a last successful
// connection from a prior process); runtime is only about this daemon process.
// With no native transport linked, a node can never be reported connected.
type federationRuntime struct {
	ID              string  `json:"id"`
	State           string  `json:"state"`
	LocalPort       *int    `json:"localPort"`
	CachedResponses int     `json:"cachedResponses"`
	LastConnectedAt *int64  `json:"lastConnectedAt"`
	LastError       *string `json:"lastError"`
	Available       bool    `json:"available"`
	Message         string  `json:"message"`
}

// federationNodeView is the HTTP-safe projection of a saved node. In
// particular it intentionally omits Node.Tunnel: until a real transport has
// allocated an ephemeral local port, serializing 127.0.0.1:<remotePort> would
// falsely imply that a local tunnel exists.
type federationNodeView struct {
	ID              string  `json:"id"`
	Label           string  `json:"label"`
	SSHTarget       string  `json:"sshTarget"`
	RemotePort      int     `json:"remotePort"`
	Enabled         bool    `json:"enabled"`
	State           string  `json:"state"`
	LastConnectedAt *int64  `json:"lastConnectedAt"`
	LastError       *string `json:"lastError"`
	CreatedAt       int64   `json:"createdAt"`
	UpdatedAt       int64   `json:"updatedAt"`
}

func (d *Daemon) federationRuntimeFor(node federation.Node) federationRuntime {
	if d.federation == nil {
		return federationRuntime{ID: node.ID, State: "error", Available: false, Message: "Federation transport is unavailable"}
	}
	return d.federation.runtime(node.ID, node)
}

func (d *Daemon) federationNodeViewFor(node federation.Node) federationNodeView {
	runtime := d.federationRuntimeFor(node)
	return federationNodeView{
		ID: node.ID, Label: node.Label, SSHTarget: node.SSHTarget,
		RemotePort: node.RemotePort, Enabled: node.Enabled, State: runtime.State,
		LastConnectedAt: node.LastConnectedAt, LastError: node.LastError,
		CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt,
	}
}

func federationNodeID(raw string) string { return strings.TrimSpace(raw) }

func (d *Daemon) federationNode(w http.ResponseWriter, id string) (*federation.Node, bool) {
	node, err := federation.Get(d.db, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return nil, false
	}
	if node == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown federation node"})
		return nil, false
	}
	return node, true
}

// handleFederation serves all /api/federation endpoints. It returns false for
// paths outside the federation namespace so the primary API router can carry
// on with its other route families.
func (d *Daemon) handleFederation(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/federation" && !strings.HasPrefix(path, "/federation/") {
		return false
	}
	if path == "/federation/nodes" {
		switch r.Method {
		case http.MethodGet:
			nodes, err := federation.List(d.db)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			} else {
				views := make([]federationNodeView, 0, len(nodes))
				for _, node := range nodes {
					views = append(views, d.federationNodeViewFor(node))
				}
				writeJSON(w, http.StatusOK, map[string]any{"nodes": views})
			}
		case http.MethodPost:
			var input federationNodeInput
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&input); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid federation node"})
				return true
			}
			id := federationNodeID(input.ID)
			if id == "" {
				var err error
				id, err = secureToken()
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not allocate federation node id"})
					return true
				}
			}
			enabled := true
			if input.Enabled != nil {
				enabled = *input.Enabled
			}
			if d.federationSecrets == nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "system secret store is unavailable"})
				return true
			}
			if err := d.federationSecrets.Set(federationSecretService, federationSecretAccount(id), input.Token); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save remote daemon capability"})
				return true
			}
			node, err := federation.Upsert(d.db, federation.Config{ID: id, Label: input.Label, SSHTarget: input.SSHTarget, RemotePort: input.RemotePort, Token: input.Token, Enabled: enabled})
			if err != nil {
				_ = d.federationSecrets.Delete(federationSecretService, federationSecretAccount(id))
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			} else {
				writeJSON(w, http.StatusCreated, map[string]any{"node": d.federationNodeViewFor(*node)})
			}
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return true
	}

	if path == "/federation/queue" && r.Method == http.MethodGet {
		nodes, err := federation.List(d.db)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return true
		}
		refresh := r.URL.Query().Get("refresh") == "true"
		rows := make([]map[string]any, 0, len(nodes))
		for _, node := range nodes {
			row := map[string]any{"node": d.federationNodeViewFor(node), "items": []any{}, "runtime": d.federationRuntimeFor(node)}
			if !node.Enabled {
				rows = append(rows, row)
				continue
			}
			token, tokenErr := d.federationToken(node.ID)
			if tokenErr != nil {
				row["error"] = "Could not load remote node capability"
				rows = append(rows, row)
				continue
			}
			items, cached, fetchErr := d.federation.queue(r.Context(), node, token, refresh)
			if fetchErr != nil {
				_ = federation.MarkRetryableFailure(d.db, node.ID, fetchErr)
				row["error"] = fetchErr.Error()
			} else {
				_ = federation.MarkConnected(d.db, node.ID)
				row["items"] = items
				row["cached"] = cached
			}
			updated, _ := federation.Get(d.db, node.ID)
			if updated != nil {
				row["node"] = d.federationNodeViewFor(*updated)
				row["runtime"] = d.federationRuntimeFor(*updated)
			}
			rows = append(rows, row)
		}
		writeJSON(w, http.StatusOK, map[string]any{"nodes": rows, "transportAvailable": true})
		return true
	}

	const prefix = "/federation/nodes/"
	if !strings.HasPrefix(path, prefix) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown federation route"})
		return true
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	id := federationNodeID(parts[0])
	if id == "" || len(parts) > 2 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown federation node route"})
		return true
	}

	if len(parts) == 1 {
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", "DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		removed, err := federation.Remove(d.db, id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		} else if !removed {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown federation node"})
		} else {
			d.federation.disconnect(id)
			if d.federationSecrets != nil {
				_ = d.federationSecrets.Delete(federationSecretService, federationSecretAccount(id))
			}
			w.WriteHeader(http.StatusNoContent)
		}
		return true
	}

	action := parts[1]
	node, ok := d.federationNode(w, id)
	if !ok {
		return true
	}
	switch action {
	case "status":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"node": d.federationNodeViewFor(*node), "runtime": d.federationRuntimeFor(*node)})
	case "disconnect":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		if err := federation.Disconnect(d.db, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return true
		}
		d.federation.disconnect(id)
		node, _ = federation.Get(d.db, id)
		writeJSON(w, http.StatusOK, map[string]any{"node": d.federationNodeViewFor(*node), "runtime": d.federationRuntimeFor(*node)})
	case "connect":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		if _, err := federation.Reconnect(d.db, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return true
		}
		token, tokenErr := d.federationToken(id)
		if tokenErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not load remote node capability"})
			return true
		}
		_, _, fetchErr := d.federation.queue(r.Context(), *node, token, true)
		if fetchErr != nil {
			_ = federation.MarkRetryableFailure(d.db, id, fetchErr)
			node, _ = federation.Get(d.db, id)
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": fetchErr.Error(), "node": d.federationNodeViewFor(*node), "runtime": d.federationRuntimeFor(*node)})
			return true
		}
		_ = federation.MarkConnected(d.db, id)
		node, _ = federation.Get(d.db, id)
		writeJSON(w, http.StatusOK, map[string]any{"node": d.federationNodeViewFor(*node), "runtime": d.federationRuntimeFor(*node)})
	case "queue", "workspaces":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		token, tokenErr := d.federationToken(id)
		if tokenErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not load remote node capability"})
			return true
		}
		refresh := r.URL.Query().Get("refresh") == "true"
		var items []map[string]any
		var cached bool
		var fetchErr error
		if action == "queue" {
			items, cached, fetchErr = d.federation.queue(r.Context(), *node, token, refresh)
		} else {
			items, cached, fetchErr = d.federation.workspaces(r.Context(), *node, token, refresh)
		}
		if fetchErr != nil {
			_ = federation.MarkRetryableFailure(d.db, id, fetchErr)
			node, _ = federation.Get(d.db, id)
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": fetchErr.Error(), "node": d.federationNodeViewFor(*node), "runtime": d.federationRuntimeFor(*node)})
			return true
		}
		_ = federation.MarkConnected(d.db, id)
		node, _ = federation.Get(d.db, id)
		writeJSON(w, http.StatusOK, map[string]any{"node": d.federationNodeViewFor(*node), "items": items, "cached": cached, "runtime": d.federationRuntimeFor(*node)})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown federation node route"})
	}
	return true
}
