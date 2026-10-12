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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAppRenameE2E drives the built binary through a rename, in a git
// repository whose remote latere is the app's push URL, against a stub of
// the Apps API with auth's mint: a slug that breaks a rule exits 1 and sends
// nothing, a refusal exits 1 with its code and leaves the remote, and a
// rename sends the new slug alone, prints the new address and push URL, and
// sets the remote.
func TestAppRenameE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	binary := latereBinary(t)
	var (
		mu       sync.Mutex
		slug     = "hello"
		taken    = "taken"
		patched  []map[string]string
		requests []string
	)
	app := func() string {
		return fmt.Sprintf(`{"id":"123e4567-e89b-42d3-a456-426614174000","slug":%[1]q,"name":"hello","owner":{"kind":"personal","id":"u-1"},"visibility":"private","state":"active","health":"none",
"url":"https://%[1]s.latere.site","preview_url":"https://next--%[1]s.latere.site",
"repository":{"clone_url":"https://code.latere.ai/r/123e4567-e89b-42d3-a456-426614174000.git","push_url":"https://code.latere.ai/u-1/%[1]s.git"}}`, slug)
	}
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
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/apps/apps/"+slug:
			_, _ = io.WriteString(w, app())
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/apps/apps/"+slug:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("rename body: %v", err)
			}
			patched = append(patched, body)
			if body["slug"] == taken {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":{"code":"slug_taken","message":"This slug is already in use.","details":{"slug":"taken","hint":"Pick another slug or leave it out to get a generated one."}}}`)
				return
			}
			slug = body["slug"]
			_, _ = io.WriteString(w, app())
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"app_not_found","message":"There is no app with this slug in this account.","details":{}}}`)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	authPath := filepath.Join(root, "auth-token.json")
	login, err := json.Marshal(map[string]any{"access_token": "saved-login", "expires_at": time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, login, 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// A git configuration of the test's own, so the developer's plays no part.
	env := append(os.Environ(), "LATERE_APPS_URL="+server.URL+"/v1/apps", "LATERE_APPS_TOKEN=", "AUTH_URL="+server.URL,
		"LATERE_AUTH_TOKEN_FILE="+authPath, "GIT_CEILING_DIRECTORIES="+root, "GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", work}, args...)...)
		command.Env = env
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	const oldPush, newPush = "https://code.latere.ai/u-1/hello.git", "https://code.latere.ai/u-1/hello-world.git"
	git("remote", "add", "latere", oldPush)
	// run answers stdout, stderr, the error, and the requests the command
	// made.
	run := func(args ...string) (string, string, []string, error) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env, command.Dir = env, work
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		mu.Lock()
		defer mu.Unlock()
		return stdout.String(), stderr.String(), slices.Clone(requests), err
	}
	exitOne := func(err error) bool {
		exit, ok := errors.AsType[*exec.ExitError](err)
		return ok && exit.ExitCode() == 1
	}

	// A slug that breaks a rule is refused before anything is sent.
	out, stderr, seen, err := run("apps", "rename", "Hello_World")
	if !exitOne(err) {
		t.Fatalf("rename to a slug that breaks a rule = %v, want exit 1", err)
	}
	if len(seen) != 0 || out != "" || !strings.Contains(stderr, `"Hello_World" cannot be an app's slug: a slug takes only lowercase letters, digits and hyphens; nothing was sent`) {
		t.Errorf("requests %v, stdout %q, stderr %q", seen, out, stderr)
	}

	// A refusal says its code, exits 1, and leaves the remote.
	out, stderr, seen, err = run("apps", "rename", "taken")
	if !exitOne(err) {
		t.Fatalf("rename to a taken slug = %v, want exit 1", err)
	}
	if !slices.Equal(seen, []string{"GET /v1/apps/apps/hello", "PATCH /v1/apps/apps/hello"}) {
		t.Errorf("requests = %v", seen)
	}
	if out != "" || !strings.Contains(stderr, "slug_taken: The slug taken is taken: another app has it, or it is held for the account that gave it up.") {
		t.Errorf("stdout %q, stderr %q", out, stderr)
	}
	if got := git("remote", "get-url", "latere"); got != oldPush {
		t.Errorf("remote latere = %q after a refusal, want it unchanged", got)
	}

	// The rename reads the slug from the remote, sends the new slug alone,
	// and sets the remote to the new push URL.
	out, stderr, seen, err = run("apps", "rename", "hello-world")
	if err != nil {
		t.Fatalf("rename: %v %q", err, stderr)
	}
	if !slices.Equal(seen, []string{"GET /v1/apps/apps/hello", "PATCH /v1/apps/apps/hello"}) {
		t.Errorf("requests = %v", seen)
	}
	mu.Lock()
	last := patched[len(patched)-1]
	mu.Unlock()
	if last["slug"] != "hello-world" || len(last) != 1 {
		t.Errorf("rename body = %v, want the slug alone", last)
	}
	for _, want := range []string{"app:        hello-world\n", "address:    https://hello-world.latere.site\n", "push:       " + newPush + "\n",
		"https://hello.latere.site stopped working", "The slug hello stays held for your account for seven days",
		"`latere apps rename hello hello-world` takes it back", "Set the git remote latere to the new push URL."} {
		if !strings.Contains(out, want) {
			t.Errorf("rename output lacks %q:\n%s", want, out)
		}
	}
	if got := git("remote", "get-url", "latere"); got != newPush {
		t.Errorf("remote latere = %q, want the new push URL", got)
	}

	// The next command finds the app by the remote the rename set.
	out, stderr, seen, err = run("apps", "rename", "hello-world", "--json")
	if err != nil {
		t.Fatalf("rename to the slug the app has: %v %q", err, stderr)
	}
	if !slices.Equal(seen, []string{"GET /v1/apps/apps/hello-world"}) {
		t.Errorf("requests = %v, want the read alone", seen)
	}
	var printed map[string]any
	if err := json.Unmarshal([]byte(out), &printed); err != nil || printed["slug"] != "hello-world" {
		t.Errorf("json = %v (%v): %q", printed, err, out)
	}
	if !strings.Contains(stderr, "hello-world has this slug already, so nothing changed.") {
		t.Errorf("stderr = %q", stderr)
	}
}
