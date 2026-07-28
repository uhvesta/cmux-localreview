package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/askruntime"
)

func btwRequest(t *testing.T, d *Daemon, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://local.test/api"+path, bytes.NewBufferString(body))
	result := httptest.NewRecorder()
	if !d.handleBtw(result, req, path) {
		t.Fatalf("route %s not handled", path)
	}
	return result
}

func prepareBtwDaemon(t *testing.T, emit bool) (*Daemon, *askRouteSession) {
	t.Helper()
	d := askRouteDaemon(t)
	result, err := d.db.Exec(`INSERT INTO sessions(label,started_at) VALUES('review',?)`, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO repos(id,workspace_relative_path,git_dir,created_at) VALUES(44,'apps/api',?,?)`, t.TempDir(), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	d.review = &workspaceReview{Root: t.TempDir(), SessionID: sessionID, Repos: []reviewRepo{{ID: "repo-1", WorkspaceRelativePath: "apps/api", DBID: 44}}}
	runtimeSession := &askRouteSession{emit: emit}
	d.askRuntime = askruntime.New(askRouteBackend{session: runtimeSession})
	return d, runtimeSession
}

func TestBtwPersistsNativeSDKThreadAndReusesItForExplicitFollowup(t *testing.T) {
	d, runtimeSession := prepareBtwDaemon(t, true)
	first := btwRequest(t, d, http.MethodPost, "/btw/ask", `{"transport":"copilot","repoId":"repo-1","filePath":"cmd/main.go","startLine":9,"endLine":10,"codeContent":"return err","question":"Why is this safe?","model":"gpt-5"}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	var payload struct {
		Thread btwThread `json:"thread"`
		Target string    `json:"target"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Thread.Transport != "copilot" || payload.Target != "copilot-sdk" || payload.Thread.CopilotSessionID == nil || len(payload.Thread.Questions) != 1 {
		t.Fatalf("payload=%#v", payload)
	}
	deadline := time.Now().Add(time.Second)
	for {
		thread, err := getBtwThread(context.Background(), d.db, payload.Thread.ID, d.review.SessionID)
		if err == nil && len(thread.Questions) == 1 && thread.Questions[0].Answer != nil && !thread.Questions[0].Answer.Pending && thread.Questions[0].Answer.Body == "Copilot reply" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("response did not settle: %#v err=%v", thread, err)
		}
		time.Sleep(time.Millisecond)
	}
	followup := btwRequest(t, d, http.MethodPost, "/btw/ask", `{"threadId":`+strconv.FormatInt(payload.Thread.ID, 10)+`,"question":"And what changes if it fails?"}`)
	if followup.Code != http.StatusAccepted {
		t.Fatalf("followup=%d %s", followup.Code, followup.Body.String())
	}
	deadline = time.Now().Add(time.Second)
	for {
		thread, err := getBtwThread(context.Background(), d.db, payload.Thread.ID, d.review.SessionID)
		if err == nil && len(thread.Questions) == 2 && thread.Questions[1].Answer != nil && !thread.Questions[1].Answer.Pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("followup did not settle: %#v err=%v", thread, err)
		}
		time.Sleep(time.Millisecond)
	}
	runtimeSession.mu.Lock()
	defer runtimeSession.mu.Unlock()
	if len(runtimeSession.prompts) != 2 || !bytes.Contains([]byte(runtimeSession.prompts[0]), []byte("Workspace-relative path: apps/api/cmd/main.go")) || !bytes.Contains([]byte(runtimeSession.prompts[0]), []byte("Selected code:")) {
		t.Fatalf("prompts=%q", runtimeSession.prompts)
	}
	listed := btwRequest(t, d, http.MethodGet, "/btw/threads", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(`"questions"`)) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
}

func TestBtwRejectsRetiredTerminalAndACPWithoutCreatingRows(t *testing.T) {
	d, _ := prepareBtwDaemon(t, false)
	for _, transport := range []string{"terminal", "acp"} {
		response := btwRequest(t, d, http.MethodPost, "/btw/ask", `{"transport":"`+transport+`","question":"hello"}`)
		if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("fallback")) && transport == "terminal" {
			t.Fatalf("%s=%d %s", transport, response.Code, response.Body.String())
		}
	}
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM btw_threads`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestBtwRequiresOpenedWorkspaceInsteadOfFocusedTerminal(t *testing.T) {
	d := askRouteDaemon(t)
	response := btwRequest(t, d, http.MethodPost, "/btw/ask", `{"question":"hello"}`)
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("focused-terminal fallback")) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
}
