// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// TestAppE2E drives the built binary against a stub of the Apps API under
// its origin base path and of auth's mint: a create from the saved login
// prints where to push, and a followed build log exits 0 when the build
// ends built and 1, with the failure's code and message on stderr, when it
// ends failed.
func TestAppE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const deployID = "5d2f8a1c-3b4e-4f6a-9c7d-8e1f2a3b4c5d"
	var (
		mu        sync.Mutex
		audiences []string
		created   map[string]string
		end       string
	)
	app := `{"id":"123e4567-e89b-42d3-a456-426614174000","slug":"hello","name":"hello","visibility":"public","state":"active","health":"none",
"url":"https://hello.latere.site","preview_url":"https://next--hello.latere.site","current_deploy":null,"latest_preview":null,
"repository":{"clone_url":"https://code.latere.ai/r/123e4567-e89b-42d3-a456-426614174000.git","push_url":"https://code.latere.ai/u-1/hello.git"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/actor-tokens" {
			var body struct {
				Audience string `json:"audience"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			audiences = append(audiences, body.Audience)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"actor_token":"app-actor","expires_in":300}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer app-actor" {
			t.Errorf("bearer = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/apps/apps":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			created = body
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, app)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/apps/apps/hello/deploys":
			_, _ = fmt.Fprintf(w, `{"deploys":[{"id":%q,"status":"building","preview":true,"ref":"refs/heads/main",
"preview_url":"https://5d2f8a1c--hello.latere.site","components":[{"name":"app","kind":"static","route":"/","status":"building"}],"created_at":"2026-09-22T12:00:00Z"}]}`, deployID)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/apps/apps/hello/deploys/"+deployID+"/logs" && r.URL.Query().Get("follow") == "1":
			w.Header().Set("Content-Type", "text/event-stream")
			mu.Lock()
			last := end
			mu.Unlock()
			_, _ = io.WriteString(w, ": heartbeat\n\n"+
				`id: app:1`+"\n"+`data: {"seq":1,"ts":"2026-09-22T12:00:01Z","src":"build","component":"app","line":"\u001b[32mnpm run build\u001b[0m"}`+"\n\n"+
				": heartbeat\n\nevent: end\ndata: "+last+"\n\n")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	authPath := filepath.Join(root, "auth-token.json")
	login, _ := json.Marshal(map[string]any{"access_token": "saved-login", "expires_at": time.Now().Add(time.Hour)})
	if err := os.WriteFile(authPath, login, 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LATERE_APPS_URL="+server.URL+"/v1/apps", "LATERE_APPS_TOKEN=", "AUTH_URL="+server.URL,
		"LATERE_AUTH_TOKEN_FILE="+authPath, "GIT_CEILING_DIRECTORIES="+root,
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
	run := func(args ...string) (string, string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env, command.Dir = env, work
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		return stdout.String(), stderr.String(), err
	}

	out, stderr, err := run("apps", "create", "--slug", "hello")
	if err != nil {
		t.Fatalf("create: %v %q", err, stderr)
	}
	mu.Lock()
	body := created
	mu.Unlock()
	if body["slug"] != "hello" || body["visibility"] != "public" {
		t.Errorf("create body = %v", body)
	}
	for _, want := range []string{"https://hello.latere.site", "https://next--hello.latere.site", "https://code.latere.ai/u-1/hello.git", "git push latere main"} {
		if !strings.Contains(out, want) {
			t.Errorf("create output lacks %q:\n%s", want, out)
		}
	}

	mu.Lock()
	end = `{"status":"built"}`
	mu.Unlock()
	out, stderr, err = run("apps", "logs", "hello", "-f")
	if err != nil {
		t.Fatalf("logs -f of a built deploy: %v %q", err, stderr)
	}
	if !strings.HasSuffix(out, "build    npm run build\n") || strings.Contains(out, "\x1b") {
		t.Errorf("logs -f stdout = %q, want the line without its color codes", out)
	}
	if !strings.Contains(stderr, "Preview: https://5d2f8a1c--hello.latere.site") {
		t.Errorf("logs -f stderr = %q", stderr)
	}

	mu.Lock()
	end = `{"status":"failed","error":{"code":"build_failed","message":"The build command failed.","hint":"Read the build log; the failing line is near the end.","component":"app"}}`
	mu.Unlock()
	out, stderr, err = run("apps", "logs", "hello", "-f")
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
		t.Fatalf("logs -f of a failed deploy = %v, want exit 1", err)
	}
	if !strings.Contains(out, "npm run build") || stderr != "deploy 5d2f8a1c failed in app: build_failed: The build command failed.\nRead the build log; the failing line is near the end.\n" {
		t.Errorf("failed logs -f stdout %q stderr %q", out, stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, aud := range audiences {
		if aud != "insula" {
			t.Errorf("minted for %q, want the Apps API's audience", aud)
		}
	}
	if len(audiences) != 3 {
		t.Errorf("mints = %v, want one per command", audiences)
	}
}
