// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// toposTestEnv points the CLI at srv with a bearer token.
func toposTestEnv(t *testing.T, srvURL string) {
	t.Helper()
	t.Setenv("TOPOS_API_URL", srvURL)
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, dir, "test-bearer"))
}

// wsFrame writes one server→client frame.
func wsFrame(ctx context.Context, c *websocket.Conn, fr map[string]any) {
	b, _ := json.Marshal(fr)
	_ = c.Write(ctx, websocket.MessageText, b)
}

// interactiveTestMux serves the interactive routes: create, list, and a WS
// attach that replays a canned turn (AssistantMessage + Stop).
func interactiveTestMux(t *testing.T, gotTurn *string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess_test", "agent_id": "ag", "status": "awaiting_input"})
	})
	mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{
			{"id": "sess_test", "agent_id": "ag", "status": "awaiting_input"},
		}})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/attach", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		// If the client sends a turn (rw print mode), read it synchronously
		// before streaming the canned reply, so the capture is deterministic.
		if gotTurn != nil {
			if _, data, rerr := c.Read(ctx); rerr == nil {
				var ctrl attachControl
				_ = json.Unmarshal(data, &ctrl)
				*gotTurn = ctrl.Text
			}
		}
		wsFrame(ctx, c, map[string]any{"type": "caught_up", "seq": 0})
		wsFrame(ctx, c, map[string]any{"type": "event", "event": "AssistantMessage", "seq": 1, "payload": json.RawMessage(`{"text":"hello from agent"}`)})
		wsFrame(ctx, c, map[string]any{"type": "event", "event": "Stop", "seq": 2, "payload": json.RawMessage(`{}`)})
	})
	return mux
}

func TestSessionLsCommand(t *testing.T) {
	mux := interactiveTestMux(t, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	toposTestEnv(t, srv.URL)

	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "session", "ls"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "sess_test") || !strings.Contains(out, "awaiting_input") {
		t.Fatalf("ls output = %q", out)
	}
}

func TestSessionStartPrintMode(t *testing.T) {
	mux := interactiveTestMux(t, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	toposTestEnv(t, srv.URL)

	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "session", "start", "ag", "-p", "do the thing"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "hello from agent") {
		t.Fatalf("print output = %q, want the streamed assistant text", out)
	}
}

func TestSessionAttachPrintModeSendsTurn(t *testing.T) {
	var gotTurn string
	mux := interactiveTestMux(t, &gotTurn)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	toposTestEnv(t, srv.URL)

	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "session", "attach", "sess_test", "-p", "add a test"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "hello from agent") {
		t.Fatalf("attach print output = %q", out)
	}
	if gotTurn != "add a test" {
		t.Fatalf("server received turn %q, want 'add a test'", gotTurn)
	}
}

func TestSessionStartFromRepoSendsFromRepo(t *testing.T) {
	var gotBody map[string]string
	mux := interactiveTestMux(t, nil)
	// Wrap the mux: intercept the create POST to capture its body, delegate the
	// rest (the attach websocket) to the canned mux.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/sessions" {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess_test", "agent_id": "ag", "status": "awaiting_input"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	toposTestEnv(t, srv.URL)

	// Use -p so the command runs non-interactively (no TUI) and exits.
	_, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "session", "start", "ag", "-p", "go", "--from-repo", "https://example.com/r.git"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotBody["from_repo"] != "https://example.com/r.git" {
		t.Fatalf("from_repo in body = %q, want the repo URL (body=%v)", gotBody["from_repo"], gotBody)
	}
}

func TestWsURLFromBase(t *testing.T) {
	if got := wsURLFromBase("https://topos.latere.ai", "sess_1", 5, false); got != "wss://topos.latere.ai/v1/sessions/sess_1/attach?since=5" {
		t.Fatalf("wss url = %q", got)
	}
	if got := wsURLFromBase("http://localhost:8080/", "sess_2", 0, true); got != "ws://localhost:8080/v1/sessions/sess_2/attach?since=0&mode=ro" {
		t.Fatalf("ws url = %q", got)
	}
}
