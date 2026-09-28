// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReposE2E drives the built binary against a stub of platformd's
// repository routes: a create is the one writer's body and prints where to
// push, the list reads the context, and a refusal exits 1 with the server's
// code and sentence on stderr and nothing on stdout.
func TestReposE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	var (
		mu      sync.Mutex
		created map[string]string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer e2e-platform" {
			t.Errorf("bearer = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repositories":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["slug"] == "taken" {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"conflict","message":"That name is already taken under this owner."}`)
				return
			}
			mu.Lock()
			created = body
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": body["id"], "owner_type": "principal", "owner_id": "p1",
				"owner_label": body["owner_label"], "slug": body["slug"], "visibility": body["visibility"]})
		case r.Method == http.MethodGet && r.URL.Path == "/repositories":
			_, _ = io.WriteString(w, `{"context":{"owner_type":"principal","owner_id":"p1","label":"alice","name":"alice"},
"repositories":[{"id":"0a1b2c3d-0000-4000-8000-000000000001","owner_label":"alice","slug":"notes","visibility":"private","role":"admin","created_at":"2026-09-20T10:00:00Z"}],"shared":[]}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	env := append(os.Environ(), "LATERE_PLATFORM_URL="+server.URL, "LATERE_PLATFORM_TOKEN=e2e-platform", "CODE_HOST=",
		"LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"),
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
	run := func(args ...string) (string, string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = env
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		return stdout.String(), stderr.String(), err
	}

	out, stderr, err := run("repos", "create", "alice/notes", "--public")
	if err != nil {
		t.Fatalf("create: %v %q", err, stderr)
	}
	mu.Lock()
	body := created
	mu.Unlock()
	if body["owner_label"] != "alice" || body["slug"] != "notes" || body["visibility"] != "public" || len(body["id"]) != 36 {
		t.Errorf("create body = %v", body)
	}
	if !strings.Contains(out, "https://code.latere.ai/alice/notes.git") || !strings.Contains(out, body["id"]) {
		t.Errorf("create output:\n%s", out)
	}

	out, stderr, err = run("repos", "list")
	if err != nil || !strings.Contains(out, "alice/notes") || !strings.Contains(out, "admin") {
		t.Errorf("list = %v %q\n%s", err, stderr, out)
	}

	out, stderr, err = run("repos", "create", "alice/taken")
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
		t.Fatalf("refused create = %v", err)
	}
	if out != "" || !strings.Contains(stderr, "conflict: That name is already taken under this owner.") {
		t.Errorf("refused create stdout %q stderr %q", out, stderr)
	}
}
