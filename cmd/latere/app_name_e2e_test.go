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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAppNameE2E drives the built binary through a change of an app's
// name against a stub of the Apps API with auth's mint: the change sends
// the name alone with the bearer minted for the Apps API, prints the app at
// the address it kept, and `rename --name` sends nothing and names the
// command that changes the name.
func TestAppNameE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	var (
		mu       sync.Mutex
		requests []string
		patched  map[string]string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/actor-tokens" {
			_, _ = io.WriteString(w, `{"actor_token":"app-actor","expires_in":300}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer app-actor" {
			t.Errorf("%s %s carried %q, want the bearer minted for the Apps API", r.Method, r.URL.Path, got)
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/apps/apps/latere-clients" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"app_not_found","message":"There is no app with this slug in this account.","details":{}}}`)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
			t.Errorf("change body: %v", err)
		}
		_, _ = io.WriteString(w, `{"id":"123e4567-e89b-42d3-a456-426614174000","slug":"latere-clients","name":`+jsonString(t, patched["name"])+`,
"owner":{"kind":"personal","id":"u-1"},"visibility":"private","state":"active","health":"ready","url":"https://latere-clients.latere.site",
"preview_url":"https://next--latere-clients.latere.site","repository":{"clone_url":"https://code.latere.ai/r/1.git","push_url":"https://code.latere.ai/u-1/latere-clients.git"}}`)
	}))
	defer server.Close()

	root := t.TempDir()
	authPath := filepath.Join(root, "auth-token.json")
	login, err := json.Marshal(map[string]any{"access_token": "saved-login", "expires_at": time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, login, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LATERE_APPS_URL="+server.URL+"/v1/apps", "LATERE_APPS_TOKEN=", "AUTH_URL="+server.URL,
		"LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root, "HOME="+root)
	run := func(args ...string) (string, string, []string, error) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env, command.Dir = env, root
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		mu.Lock()
		defer mu.Unlock()
		return stdout.String(), stderr.String(), slices.Clone(requests), err
	}

	out, stderr, seen, err := run("apps", "name", "Latere design boards", "latere-clients")
	if err != nil {
		t.Fatalf("name: %v %q", err, stderr)
	}
	if !slices.Equal(seen, []string{"PATCH /v1/apps/apps/latere-clients"}) {
		t.Errorf("requests = %v", seen)
	}
	mu.Lock()
	body := patched
	mu.Unlock()
	if len(body) != 1 || body["name"] != "Latere design boards" {
		t.Errorf("change body = %v, want the name alone", body)
	}
	if !strings.Contains(out, "name:       Latere design boards\n") || !strings.Contains(out, "address:    https://latere-clients.latere.site\n") {
		t.Errorf("name output:\n%s", out)
	}

	_, stderr, seen, err = run("apps", "rename", "--name", "Latere design boards", "latere-clients")
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
		t.Fatalf("rename --name = %v", err)
	}
	if len(seen) != 0 || !strings.Contains(stderr, `latere apps name "Latere design boards" latere-clients`) {
		t.Errorf("rename --name sent %v and said:\n%s", seen, stderr)
	}
}

// jsonString is s as a JSON string.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
