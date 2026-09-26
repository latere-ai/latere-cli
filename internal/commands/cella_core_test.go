// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"

	cellaclient "latere.ai/x/cella/client"
)

// corePrefix is the path the hosted control plane serves its /v1 routes
// under, which the CLI's base URL carries.
const corePrefix = "/v1/environments"

// coreToken is the bearer every command presents through LATERE_CELLA_TOKEN.
const coreToken = "core-token"

// coreRequest is one request the fake core received.
type coreRequest struct {
	Method, Path, ContentType, Auth string
	Query                           map[string][]string
	Body                            []byte
}

// String is the request as a test compares it: the method, the path under
// the prefix, and the query.
func (r coreRequest) String() string {
	var q []string
	for k, vs := range r.Query {
		for _, v := range vs {
			q = append(q, k+"="+v)
		}
	}
	sort.Strings(q)
	s := r.Method + " " + r.Path
	if len(q) > 0 {
		s += "?" + strings.Join(q, "&")
	}
	return s
}

// fakeCore is a Cella control plane on one httptest server, serving the
// routes the commands reach under corePrefix in the shapes of the exported
// client: a create answers 201 Pending, or with wait=1 the phase heldPhase,
// the list pages by cursor, and a refusal is the error envelope.
type fakeCore struct {
	t   *testing.T
	srv *httptest.Server

	// heldPhase and heldReason are what a create held with wait=1 answers.
	heldPhase, heldReason string
	// exec is the synchronous exec's answer.
	exec cellaclient.ExecResult
	// deleteStatus refuses a delete with that status when set.
	deleteStatus int
	// dropCreate closes the connection of a create without an answer.
	dropCreate bool
	// listed are the sandbox names the list answers, split over pages of
	// pageSize.
	listed   []string
	pageSize int
	// exportFailure is the trailer code an export ends with, when set.
	exportFailure string
	// attach runs the server's side of the attach socket.
	attach func(*websocket.Conn)

	mu       sync.Mutex
	requests []coreRequest
}

func newFakeCore(t *testing.T) *fakeCore {
	t.Helper()
	t.Setenv("LATERE_CELLA_TOKEN", coreToken)
	t.Setenv("LATERE_CELLA_URL", "")
	f := &fakeCore{t: t, heldPhase: cellaRunning, pageSize: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// url is the base the commands are pointed at.
func (f *fakeCore) url() string { return f.srv.URL + corePrefix }

// seen is every request so far, as coreRequest.String renders it.
func (f *fakeCore) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.requests))
	for i, r := range f.requests {
		out[i] = r.String()
	}
	return out
}

// all is a copy of every request so far.
func (f *fakeCore) all() []coreRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]coreRequest(nil), f.requests...)
}

// last is the most recent request whose method and path are given.
func (f *fakeCore) last(method, path string) coreRequest {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range slices.Backward(f.requests) {
		if r.Method == method && r.Path == path {
			return r
		}
	}
	f.t.Fatalf("no %s %s among %v", method, path, f.requests)
	return coreRequest{}
}

// coreSandbox is a sandbox object as the control plane renders it.
func coreSandbox(name, phase, reason string) map[string]any {
	status := map[string]any{
		"id": "sbx-" + name, "phase": phase,
		"createdAt": time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339),
	}
	if reason != "" {
		status["reason"] = reason
	}
	return map[string]any{
		"apiVersion": "cella.latere.ai/v1beta1", "kind": "Sandbox",
		"metadata": map[string]any{"name": name},
		"spec":     map[string]any{"image": "base", "resources": map[string]any{"cpu": "2", "memory": "4Gi"}},
		"status":   status,
	}
}

func (f *fakeCore) answer(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("write answer: %v", err)
	}
}

func (f *fakeCore) refuse(w http.ResponseWriter, status int, code, message string) {
	f.answer(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func (f *fakeCore) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, corePrefix+"/")
	if !ok {
		f.t.Errorf("request outside %s: %s %s", corePrefix, r.Method, r.URL.Path)
		f.refuse(w, http.StatusNotFound, "not_found", "No such route.")
		return
	}
	req := coreRequest{
		Method: r.Method, Path: "/" + rest, Query: r.URL.Query(),
		ContentType: r.Header.Get("Content-Type"), Auth: r.Header.Get("Authorization"),
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 3 && parts[2] == "attach" {
		f.record(req)
		f.serveAttach(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("read %s %s: %v", r.Method, r.URL.Path, err)
		return
	}
	req.Body = body
	f.record(req)
	if req.Auth != "Bearer "+coreToken {
		f.refuse(w, http.StatusUnauthorized, "unauthenticated", "Sign in and send a valid token.")
		return
	}
	if parts[0] != "sandboxes" {
		f.refuse(w, http.StatusNotFound, "not_found", "No such route.")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		f.serveList(w, r)
	case len(parts) == 1 && r.Method == http.MethodPost:
		f.serveCreate(w, r, manifestNameOf(body))
	case len(parts) == 2 && r.Method == http.MethodPut:
		f.serveCreate(w, r, parts[1])
	case len(parts) == 2 && r.Method == http.MethodGet:
		f.answer(w, http.StatusOK, coreSandbox(parts[1], cellaRunning, ""))
	case len(parts) == 2 && r.Method == http.MethodDelete:
		if f.deleteStatus != 0 {
			f.refuse(w, f.deleteStatus, "internal", "The delete failed.")
			return
		}
		f.answer(w, http.StatusOK, coreSandbox(parts[1], "Deleting", ""))
	case len(parts) == 3 && parts[2] == "start":
		f.answer(w, http.StatusOK, coreSandbox(parts[1], cellaRunning, ""))
	case len(parts) == 3 && parts[2] == "stop":
		f.answer(w, http.StatusOK, coreSandbox(parts[1], "Stopped", ""))
	case len(parts) == 3 && parts[2] == "exec":
		f.answer(w, http.StatusOK, f.exec)
	case len(parts) == 3 && parts[2] == "logs":
		_, _ = io.WriteString(w, "main process output\n")
	case len(parts) >= 3 && parts[2] == "files":
		f.serveFiles(w, r, strings.Join(parts[3:], "/"))
	default:
		f.refuse(w, http.StatusNotFound, "not_found", "No such route.")
	}
}

func (f *fakeCore) record(r coreRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
}

// manifestNameOf is the name a create's manifest declares, or a name the
// server picks.
func manifestNameOf(body []byte) string {
	var m struct {
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if yaml.Unmarshal(body, &m) != nil || m.Metadata.Name == "" {
		return "generated"
	}
	return m.Metadata.Name
}

func (f *fakeCore) serveCreate(w http.ResponseWriter, r *http.Request, name string) {
	if f.dropCreate {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			f.t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
		return
	}
	phase, reason := cellaPending, ""
	if r.URL.Query().Get("wait") == "1" {
		phase, reason = f.heldPhase, f.heldReason
	}
	f.answer(w, http.StatusCreated, coreSandbox(name, phase, reason))
}

func (f *fakeCore) serveList(w http.ResponseWriter, r *http.Request) {
	start := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		for i, n := range f.listed {
			if n == c {
				start = i
			}
		}
	}
	end := min(start+f.pageSize, len(f.listed))
	items := []any{}
	for _, n := range f.listed[start:end] {
		items = append(items, coreSandbox(n, cellaRunning, ""))
	}
	next := ""
	if end < len(f.listed) {
		next = f.listed[end]
	}
	f.answer(w, http.StatusOK, map[string]any{"items": items, "next": next})
}

// tarEntries reads an archive into its file names and contents.
func tarEntries(t *testing.T, body []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(body))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(content)
	}
}

func (f *fakeCore) serveFiles(w http.ResponseWriter, r *http.Request, op string) {
	switch {
	case op == "list" && r.Method == http.MethodGet:
		f.answer(w, http.StatusOK, map[string]any{"items": []map[string]any{
			{"name": "src", "path": r.URL.Query().Get("path") + "/src", "size": 96, "mode": "0755", "isDir": true},
			{"name": "go.mod", "path": r.URL.Query().Get("path") + "/go.mod", "size": 42, "mode": "0644"},
		}})
	case op == "content" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, "file content\n")
	case op == "mkdir" || op == "move":
		w.WriteHeader(http.StatusNoContent)
	case op == "" && r.Method == http.MethodGet:
		// An export: a one-entry archive, with the failure after its first
		// byte in the trailer when the case asks for one.
		if f.exportFailure != "" {
			w.Header().Set("Trailer", "X-Cella-Error")
		}
		w.Header().Set("Content-Type", "application/x-tar")
		tw := tar.NewWriter(w)
		content := "package main\n"
		if err := tw.WriteHeader(&tar.Header{Name: "workspace/main.go", Mode: 0o644, Size: int64(len(content))}); err != nil {
			f.t.Errorf("write export header: %v", err)
			return
		}
		if _, err := io.WriteString(tw, content); err != nil {
			f.t.Errorf("write export: %v", err)
			return
		}
		if f.exportFailure != "" {
			w.(http.Flusher).Flush()
			w.Header().Set("X-Cella-Error", f.exportFailure)
			return
		}
		if err := tw.Close(); err != nil {
			f.t.Errorf("close export: %v", err)
		}
	case op == "" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		w.WriteHeader(http.StatusNoContent)
	default:
		f.refuse(w, http.StatusNotFound, "not_found", "No such route.")
	}
}

// socketRequest is the attach socket's first frame, as the server reads it.
type socketRequest struct {
	Command []string `json:"command"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
}

func (f *fakeCore) serveAttach(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+coreToken {
		f.refuse(w, http.StatusUnauthorized, "unauthenticated", "Sign in and send a valid token.")
		return
	}
	upgrader := websocket.Upgrader{Subprotocols: []string{"cella.exec.v1"}, CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		f.t.Errorf("upgrade: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()
	if f.attach == nil {
		f.t.Error("attach socket opened with no session for it")
		return
	}
	f.attach(conn)
}

// runCella runs one `latere cella` command against the fake core. The base
// URL flag goes right after the subcommand's name, ahead of any argv after
// --.
func (f *fakeCore) runCella(stdin string, args ...string) (stdout, stderr string, err error) {
	cmd := newCellaCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	full := append([]string{args[0], "--api-url", f.url()}, args[1:]...)
	cmd.SetArgs(full)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// exitCode is the process exit code the root would map err to.
func exitCode(err error) int {
	return HandleExitError(io.Discard, err)
}

// tarOf is an archive of the given files.
func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
