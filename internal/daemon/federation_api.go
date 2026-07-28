package daemon

// This file deliberately exposes federation configuration before the SSH
// transport is ported.  It never dials a configured target and never invents
// a tunnel/connection: Queue Home can safely manage durable node metadata,
// while every unavailable remote operation says so explicitly.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/federation"
)

const federationTransportUnavailable = "Remote federation transport is not available in this build. The node configuration is saved; install a release with SSH federation support before connecting or loading its queue."

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

func federationRuntimeFor(node federation.Node) federationRuntime {
	state := "disconnected"
	if !node.Enabled {
		state = "disabled"
	} else if node.LastError != nil && strings.TrimSpace(*node.LastError) != "" {
		state = "error"
	}
	return federationRuntime{
		ID: node.ID, State: state, LocalPort: nil, CachedResponses: 0,
		LastConnectedAt: node.LastConnectedAt, LastError: node.LastError,
		Available: false, Message: federationTransportUnavailable,
	}
}

func federationNodeViewFor(node federation.Node) federationNodeView {
	runtime := federationRuntimeFor(node)
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
					views = append(views, federationNodeViewFor(node))
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
			node, err := federation.Upsert(d.db, federation.Config{ID: id, Label: input.Label, SSHTarget: input.SSHTarget, RemotePort: input.RemotePort, Token: input.Token, Enabled: enabled})
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			} else {
				writeJSON(w, http.StatusCreated, map[string]any{"node": federationNodeViewFor(*node)})
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
		// Preserve the TS response's per-node aggregate shape, but make lack of
		// transport explicit instead of emitting a misleading empty success.
		rows := make([]map[string]any, 0, len(nodes))
		for _, node := range nodes {
			row := map[string]any{"node": federationNodeViewFor(node), "items": []any{}, "runtime": federationRuntimeFor(node)}
			if node.Enabled {
				row["error"] = federationTransportUnavailable
			}
			rows = append(rows, row)
		}
		writeJSON(w, http.StatusOK, map[string]any{"nodes": rows, "transportAvailable": false})
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
		writeJSON(w, http.StatusOK, map[string]any{"node": federationNodeViewFor(*node), "runtime": federationRuntimeFor(*node)})
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
		node, _ = federation.Get(d.db, id)
		writeJSON(w, http.StatusOK, map[string]any{"node": federationNodeViewFor(*node), "runtime": federationRuntimeFor(*node)})
	case "connect":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		// Re-enable only. Reporting success from an unimplemented transport is
		// worse than a clear recovery instruction, so this remains a 501.
		if _, err := federation.Reconnect(d.db, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return true
		}
		node, _ = federation.Get(d.db, id)
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": federationTransportUnavailable, "node": federationNodeViewFor(*node), "runtime": federationRuntimeFor(*node)})
	case "queue", "workspaces":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": federationTransportUnavailable, "node": federationNodeViewFor(*node), "runtime": federationRuntimeFor(*node)})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown federation node route"})
	}
	return true
}
