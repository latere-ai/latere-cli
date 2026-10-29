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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// e2eCore is a Cella control plane under /v1/environments for the built
// binary: a create answers 201 Pending, or Running when held with wait=1, a
// command exits with the code the case sets, and every request is recorded.
type e2eCore struct {
	srv *httptest.Server
	// exitCode is read by the server while the test, which shares no
	// memory with the binary's requests, sets it between runs.
	exitCode atomic.Int32

	mu   sync.Mutex
	seen []string
	file string
	// created is the body of each create, which carries the account's
	// labels the platform answered.
	created [][]byte
	// secretWrites is the body of each write of a Secret.
	secretWrites [][]byte
}

// e2eLabels is what the platform's labels read answers the binary.
const e2eLabels = `{"tenant":{"labels":{"platform.latere.ai/tenant":"p-0123456789abcdef0123456789abcdef","platform.latere.ai/principal":"p1"}}}`

func newE2ECore(t *testing.T) *e2eCore {
	t.Helper()
	c := &e2eCore{}
	upgrader := websocket.Upgrader{Subprotocols: []string{"cella.exec.v1"}}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/account/environments/context" {
			if r.Header.Get("Authorization") != "Bearer e2e-platform-token" {
				t.Errorf("labels read with %q, want the platform's bearer", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, e2eLabels)
			return
		}
		if rest, ok := strings.CutPrefix(r.URL.Path, "/v1/environments/secrets"); ok && r.Header.Get("Authorization") == "Bearer e2e-token" {
			c.serveSecret(t, w, r, rest)
			return
		}
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
			body, _ := io.ReadAll(r.Body)
			c.mu.Lock()
			c.created = append(c.created, body)
			c.mu.Unlock()
			phase := "Pending"
			if r.URL.Query().Get("wait") == "1" {
				phase = "Running"
			}
			answer(http.StatusCreated, sandbox(parts[0], phase))
		case len(parts) == 1 && r.Method == http.MethodDelete:
			answer(http.StatusOK, sandbox(parts[0], "Deleting"))
		case len(parts) == 2 && parts[1] == "exec":
			answer(http.StatusOK, map[string]any{"exitCode": c.exitCode.Load(), "stdout": "ran\n"})
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

// serveSecret is the core's Secret routes for one secret, github-token,
// which one workload attaches. No answer carries a value.
func (c *e2eCore) serveSecret(t *testing.T, w http.ResponseWriter, r *http.Request, rest string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read %s %s: %v", r.Method, r.URL.Path, err)
		return
	}
	c.mu.Lock()
	c.seen = append(c.seen, r.Method+" secrets"+rest)
	if len(body) > 0 {
		c.secretWrites = append(c.secretWrites, body)
	}
	c.mu.Unlock()
	secret := func(version int) map[string]any {
		return map[string]any{
			"apiVersion": "cella.latere.ai/v1beta1", "kind": "Secret",
			"metadata": map[string]any{"name": "github-token", "labels": map[string]any{
				"platform.latere.ai/tenant": "p-0123456789abcdef0123456789abcdef", "platform.latere.ai/principal": "p1"}},
			"spec": map[string]any{"kind": "static", "scope": map[string]any{"hosts": []any{"api.github.com"}, "ports": []any{443}},
				"inject": map[string]any{"header": "Authorization", "scheme": "bearer"}},
			"status": map[string]any{"id": "sec_1", "version": version, "mountedBy": 1},
		}
	}
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case rest == "" && r.Method == http.MethodPost:
		answer(http.StatusCreated, secret(1))
	case rest == "" && r.Method == http.MethodGet:
		answer(http.StatusOK, map[string]any{"items": []any{secret(1)}})
	case rest == "/github-token" && r.Method == http.MethodPut:
		answer(http.StatusOK, secret(2))
	case rest == "/github-token":
		answer(http.StatusOK, secret(1))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
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
	return c.runWith(t, stdin, []string{"LATERE_ENVIRONMENTS_URL=" + c.srv.URL + "/v1/environments", "LATERE_ENVIRONMENTS_TOKEN=e2e-token",
		"LATERE_PLATFORM_URL=" + c.srv.URL, "LATERE_PLATFORM_TOKEN=e2e-platform-token"}, args...)
}

// runWith is run with the given variables in place of the control plane's
// address and bearer.
func (c *e2eCore) runWith(t *testing.T, stdin string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, latereBinary(t), args...)
	cmd.Stdin = strings.NewReader(stdin)
	root := t.TempDir()
	cmd.Env = append(append(os.Environ(), env...),
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

// The built binary drives a workload's life as `latere environments`: a held
// apply, the list, a command with its exit code, a file round trip, a
// terminal, and the delete.
func TestEnvironmentsOnTheCoreE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	manifest := filepath.Join(t.TempDir(), "workload.yaml")
	if err := os.WriteFile(manifest, []byte("apiVersion: cella.latere.ai/v1beta1\nkind: Sandbox\nmetadata:\n  name: dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := c.run(t, "", "environments", "apply", "-f", manifest, "--wait"); code != 0 || !strings.Contains(out, "Running") {
		t.Fatalf("apply --wait = %d %q %q", code, out, errOut)
	}
	if out, _, code := c.run(t, "", "environments", "list"); code != 0 || !strings.Contains(out, "workload:") || !strings.Contains(out, "sbx-dev") {
		t.Fatalf("list = %d %q", code, out)
	}
	c.exitCode.Store(5)
	if out, _, code := c.run(t, "", "environments", "exec", "dev", "--", "false"); code != 5 || out != "ran\n" {
		t.Fatalf("exec = %d %q, want the command's exit code 5", code, out)
	}
	if _, errOut, code := c.run(t, "hello", "environments", "write", "dev", "note.txt"); code != 0 {
		t.Fatalf("write = %d %q", code, errOut)
	}
	if out, _, code := c.run(t, "", "environments", "cat", "dev", "note.txt"); code != 0 || out != "hello" {
		t.Fatalf("cat = %d %q", code, out)
	}
	if out, _, code := c.run(t, "ls\n", "environments", "shell", "dev"); code != 4 || out != "echo: ls\n" {
		t.Fatalf("shell = %d %q, want the shell's exit code 4", code, out)
	}
	if _, errOut, code := c.run(t, "", "environments", "delete", "dev"); code != 0 || !strings.Contains(errOut, "deleted dev") {
		t.Fatalf("delete = %d %q", code, errOut)
	}
	want := []string{"PUT /dev", "GET ", "POST /dev/exec", "PUT /dev/files", "GET /dev/files/content", "GET /dev/attach", "DELETE /dev"}
	if got := c.requests(); !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	c.hasLabels(t)
}

// hasLabels fails the test unless every create carried the account's
// labels, which the platform refuses a create without.
func (c *e2eCore) hasLabels(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.created) == 0 {
		t.Fatal("no create was made")
	}
	for _, body := range c.created {
		if !strings.Contains(string(body), "platform.latere.ai/tenant") || !strings.Contains(string(body), "p-0123456789abcdef0123456789abcdef") || !strings.Contains(string(body), "platform.latere.ai/principal") {
			t.Errorf("create body %q carries no account labels", body)
		}
	}
}

// The built binary creates a secret from flags with its value piped in,
// lists it, rotates it and deletes it. Each write carries the account's
// labels and the value, and no output carries the value.
func TestEnvironmentsSecretsOnTheCoreE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	const value, rotated = "ghp_e2eFirstSECRET", "ghp_e2eSecondSECRET"
	var outputs []string
	out, errOut, code := c.run(t, value+"\n", "environments", "secrets", "create", "github-token", "--host", "api.github.com")
	if code != 0 || !strings.Contains(out, "sec_1") {
		t.Fatalf("create = %d %q %q", code, out, errOut)
	}
	outputs = append(outputs, out, errOut)
	out, errOut, code = c.run(t, "", "environments", "secrets", "list")
	if code != 0 || !strings.Contains(out, "as a bearer token in the Authorization header") || !strings.Contains(out, "to 1 workload") {
		t.Fatalf("list = %d %q %q", code, out, errOut)
	}
	outputs = append(outputs, out, errOut)
	out, errOut, code = c.run(t, rotated, "environments", "secrets", "rotate", "github-token")
	if code != 0 || !strings.Contains(out, "version:    2") || !strings.Contains(errOut, "rotated github-token") {
		t.Fatalf("rotate = %d %q %q", code, out, errOut)
	}
	outputs = append(outputs, out, errOut)
	out, errOut, code = c.run(t, "", "environments", "secrets", "delete", "github-token")
	if code != 0 || !strings.Contains(errOut, "deleted secret github-token") || !strings.Contains(errOut, "1 workload attached it") {
		t.Fatalf("delete = %d %q %q", code, out, errOut)
	}
	outputs = append(outputs, out, errOut)
	want := []string{"POST secrets", "GET secrets", "GET secrets/github-token", "PUT secrets/github-token", "DELETE secrets/github-token"}
	if got := c.requests(); !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	c.mu.Lock()
	writes := append([][]byte(nil), c.secretWrites...)
	c.mu.Unlock()
	if len(writes) != 2 {
		t.Fatalf("secret writes = %d, want the create and the rotation", len(writes))
	}
	for i, v := range []string{value, rotated} {
		var w struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Value string `json:"value"`
			} `json:"spec"`
			Status any `json:"status"`
		}
		if err := json.Unmarshal(writes[i], &w); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if w.Spec.Value != v || w.Metadata.Labels["platform.latere.ai/tenant"] != "p-0123456789abcdef0123456789abcdef" || w.Status != nil {
			t.Errorf("write %d = %s, want the value with the account's labels and no status", i, writes[i])
		}
	}
	for _, text := range outputs {
		if strings.Contains(text, value) || strings.Contains(text, rotated) {
			t.Errorf("a value is in the output:\n%s", text)
		}
	}
}

// run --ephemeral --rm creates, runs and deletes, and exits with the
// command's code.
func TestEnvironmentsOneShotOnTheCoreE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	c.exitCode.Store(2)
	out, errOut, code := c.run(t, "", "environments", "run", "--ephemeral", "--rm", "--", "false")
	if code != 2 || out != "ran\n" || !strings.Contains(errOut, "deleted workload run-") {
		t.Fatalf("run = %d %q %q", code, out, errOut)
	}
	got := c.requests()
	if len(got) != 3 || !strings.HasPrefix(got[0], "PUT /run-") || !strings.HasSuffix(got[1], "/exec") || !strings.HasPrefix(got[2], "DELETE /run-") {
		t.Errorf("requests = %v, want create, exec, delete", got)
	}
	c.hasLabels(t)
}

// A command of the retired API exits 1 and says why it is gone.
func TestRemovedCellaCommandE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	_, errOut, code := c.run(t, "", "environments", "policy", "list")
	if code != 1 || !strings.Contains(errOut, "'latere environments policy' is no longer available") {
		t.Fatalf("policy = %d %q", code, errOut)
	}
	if got := c.requests(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

// The words named after the open cores (spec 011) are unknown commands:
// each exits 1 saying so and reaches nothing.
func TestRetiredCommandWordsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	c := newE2ECore(t)
	for _, args := range [][]string{{"cella", "list"}, {"sandbox", "list"}, {"topos", "--local"}, {"review"}, {"app", "list"}} {
		out, errOut, code := c.run(t, "", args...)
		if code != 1 || out != "" || !strings.Contains(errOut, `unknown command "`+args[0]+`"`) {
			t.Errorf("%v = %d %q %q, want exit 1 as an unknown command", args, code, out, errOut)
		}
	}
	if got := c.requests(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}
