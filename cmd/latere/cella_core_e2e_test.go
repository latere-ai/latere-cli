// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// e2eCore is a Cella control plane under /v1/environments for the built
// binary: a create answers 201 Pending, or Running when held with wait=1, a
// command exits with the code the case sets, and every request is recorded.
type e2eCore struct {
	srv      *httptest.Server
	exitCode int

	mu   sync.Mutex
	seen []string
	file string
}

func newE2ECore(t *testing.T) *e2eCore {
	t.Helper()
	c := &e2eCore{}
	upgrader := websocket.Upgrader{Subprotocols: []string{"cella.exec.v1"}}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/v1/environments/sandboxes")
		if !ok || r.Header.Get("Authorization") != "Bearer e2e-token" {
			t.Errorf("unexpected request %s %s (%q)", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNotFound)
			return
		}
		c.mu.Lock()
		c.seen = append(c.seen, r.Method+" "+rest)
		c.mu.Unlock()
		sandbox := func(name, phase string) map[string]any {
			return map[string]any{"kind": "Sandbox", "metadata": map[string]any{"name": name}, "status": map[string]any{"id": "sbx-" + name, "phase": phase}}
		}
		answer := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
		switch {
		case rest == "" && r.Method == http.MethodGet:
			answer(http.StatusOK, map[string]any{"items": []any{sandbox("dev", "Running")}})
		case len(parts) == 1 && r.Method == http.MethodPut:
			phase := "Pending"
			if r.URL.Query().Get("wait") == "1" {
				phase = "Running"
			}
			answer(http.StatusCreated, sandbox(parts[0], phase))
		case len(parts) == 1 && r.Method == http.MethodDelete:
			answer(http.StatusOK, sandbox(parts[0], "Deleting"))
		case len(parts) == 2 && parts[1] == "exec":
			answer(http.StatusOK, map[string]any{"exitCode": c.exitCode, "stdout": "ran\n"})
		case len(parts) == 2 && parts[1] == "files" && r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			c.mu.Lock()
			c.file = string(body)
			c.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 3 && parts[2] == "content":
			c.mu.Lock()
			_, _ = io.WriteString(w, c.file)
			c.mu.Unlock()
		case len(parts) == 2 && parts[1] == "attach":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Errorf("attach request: %v", err)
				return
			}
			_, input, err := conn.ReadMessage()
			if err != nil {
				t.Errorf("attach input: %v", err)
				return
			}
			_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte("echo: "), input...))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"exit":4}`))
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *e2eCore) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// run runs the built binary against the core and returns its output and exit
// code.
func (c *e2eCore) run(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, latereBinary(t), args...)
	cmd.Stdin = strings.NewReader(stdin)
	root := t.TempDir()
	cmd.Env = append(os.Environ(), "LATERE_CELLA_URL="+c.srv.URL+"/v1/environments", "LATERE_CELLA_TOKEN=e2e-token",
		"LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root,
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return out.String(), errOut.String(), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), errOut.String(), 0
}

// The built binary drives a sandbox's life on the core: a held apply, the
// list, a command with its exit code, a file round trip, a terminal, and the
// delete.
func TestCellaOnTheCoreE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	manifest := filepath.Join(t.TempDir(), "sandbox.yaml")
	if err := os.WriteFile(manifest, []byte("apiVersion: cella.latere.ai/v1beta1\nkind: Sandbox\nmetadata:\n  name: dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := c.run(t, "", "cella", "apply", "-f", manifest, "--wait"); code != 0 || !strings.Contains(out, "Running") {
		t.Fatalf("apply --wait = %d %q %q", code, out, errOut)
	}
	if out, _, code := c.run(t, "", "sandbox", "list"); code != 0 || !strings.Contains(out, "sbx-dev") {
		t.Fatalf("list through the sandbox alias = %d %q", code, out)
	}
	c.exitCode = 5
	if out, _, code := c.run(t, "", "cella", "exec", "dev", "--", "false"); code != 5 || out != "ran\n" {
		t.Fatalf("exec = %d %q, want the command's exit code 5", code, out)
	}
	if _, errOut, code := c.run(t, "hello", "cella", "write", "dev", "note.txt"); code != 0 {
		t.Fatalf("write = %d %q", code, errOut)
	}
	if out, _, code := c.run(t, "", "cella", "cat", "dev", "note.txt"); code != 0 || out != "hello" {
		t.Fatalf("cat = %d %q", code, out)
	}
	if out, _, code := c.run(t, "ls\n", "cella", "shell", "dev"); code != 4 || out != "echo: ls\n" {
		t.Fatalf("shell = %d %q, want the shell's exit code 4", code, out)
	}
	if _, errOut, code := c.run(t, "", "cella", "delete", "dev"); code != 0 || !strings.Contains(errOut, "deleted dev") {
		t.Fatalf("delete = %d %q", code, errOut)
	}
	want := []string{"PUT /dev", "GET ", "POST /dev/exec", "PUT /dev/files", "GET /dev/files/content", "GET /dev/attach", "DELETE /dev"}
	if got := c.requests(); !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// run --ephemeral --rm creates, runs and deletes, and exits with the
// command's code.
func TestCellaOneShotOnTheCoreE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	c.exitCode = 2
	out, errOut, code := c.run(t, "", "cella", "run", "--ephemeral", "--rm", "--", "false")
	if code != 2 || out != "ran\n" || !strings.Contains(errOut, "deleted cella run-") {
		t.Fatalf("run = %d %q %q", code, out, errOut)
	}
	got := c.requests()
	if len(got) != 3 || !strings.HasPrefix(got[0], "PUT /run-") || !strings.HasSuffix(got[1], "/exec") || !strings.HasPrefix(got[2], "DELETE /run-") {
		t.Errorf("requests = %v, want create, exec, delete", got)
	}
}

// A command of the retired API exits 1 and says why it is gone.
func TestRemovedCellaCommandE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	_, errOut, code := c.run(t, "", "cella", "policy", "list")
	if code != 1 || !strings.Contains(errOut, "'latere cella policy' is no longer available") {
		t.Fatalf("policy = %d %q", code, errOut)
	}
	if got := c.requests(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}
