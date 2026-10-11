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

// TestAppAccessE2E drives the built binary through deciding who can open an
// app, against a stub of the Apps API with auth's mint and a stub of the
// platform on a server of its own: the visibility changes at the Apps API
// once the platform has said whether the app has a password, public is
// refused before any request, and an invitation is made, listed and taken
// back at the platform, each API with a bearer minted for it alone.
func TestAppAccessE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const appID = "123e4567-e89b-42d3-a456-426614174000"
	var (
		mu         sync.Mutex
		visibility = "unlisted"
		patched    map[string]string
		invited    []string
		requests   []string
	)
	app := func() string {
		return fmt.Sprintf(`{"id":%q,"slug":"hello","name":"hello","owner":{"kind":"personal","id":"u-1"},"visibility":%q,"state":"active","health":"none",
"url":"https://hello.latere.site","preview_url":"https://next--hello.latere.site",
"repository":{"clone_url":"https://code.latere.ai/r/%s.git","push_url":"https://code.latere.ai/u-1/hello.git"}}`, appID, visibility, appID)
	}
	record := func(r *http.Request, bearer string) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+bearer {
			t.Errorf("%s %s carried %q, want the bearer minted for this API", r.Method, r.URL.Path, got)
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
	}
	apps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/actor-tokens" {
			var body struct {
				Audience string `json:"audience"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = fmt.Fprintf(w, `{"actor_token":"actor-for-%s","expires_in":300}`, body.Audience)
			return
		}
		record(r, "actor-for-insula")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/apps/apps/hello":
			_, _ = io.WriteString(w, app())
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/apps/apps/hello":
			patched = nil
			_ = json.NewDecoder(r.Body).Decode(&patched)
			visibility = patched["visibility"]
			_, _ = io.WriteString(w, app())
		default:
			t.Errorf("unexpected request at the Apps API: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apps.Close()
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		record(r, "actor-for-api.latere.ai")
		base := "/v1/account/apps/" + appID + "/access"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base:
			var rows []string
			for i, email := range invited {
				rows = append(rows, fmt.Sprintf(`{"id":"inv_%d","role":"viewer","email":%q,"state":"pending","invited_at":%q}`, i, email, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339)))
			}
			_, _ = fmt.Fprintf(w, `{"app_id":%q,"people":[%s],"organization":null,"actions":["invite","revoke"],"limits":{"max_people":50,"max_emails_per_invite":20}}`, appID, strings.Join(rows, ","))
		case r.Method == http.MethodPost && r.URL.Path == base+"/invites":
			var body struct {
				Emails []string `json:"emails"`
				Role   string   `json:"role"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Role != "viewer" {
				t.Errorf("invite role = %q", body.Role)
			}
			invited = append(invited, body.Emails...)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"people":[{"id":"inv_0","role":"viewer","email":%q,"state":"pending","invited_at":"2026-10-07T10:10:00Z"}],"skipped":[{"email":"me@example.com","reason":"self"}]}`, body.Emails[0])
		case r.Method == http.MethodDelete && r.URL.Path == base+"/inv_0":
			invited = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request at the platform: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer platform.Close()

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
	env := append(os.Environ(), "LATERE_APPS_URL="+apps.URL+"/v1/apps", "LATERE_APPS_TOKEN=", "LATERE_PLATFORM_URL="+platform.URL, "LATERE_PLATFORM_TOKEN=",
		"AUTH_URL="+apps.URL, "LATERE_AUTH_TOKEN_FILE="+authPath, "GIT_CEILING_DIRECTORIES="+root,
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
	// run answers stdout, stderr, the error, and the requests the command
	// made at either API.
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
	wantAll := func(what, got string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %q:\n%s", what, want, got)
			}
		}
	}

	out, stderr, made, err := run("apps", "visibility", "private", "hello")
	if err != nil {
		t.Fatalf("visibility private: %v %q", err, stderr)
	}
	// Whether the app has a password is read at the platform before the
	// change, and this platform sets none.
	if !slices.Equal(made, []string{"GET /v1/apps/apps/hello", "GET /v1/account/apps/" + appID + "/access", "PATCH /v1/apps/apps/hello"}) || len(patched) != 1 || patched["visibility"] != "private" {
		t.Errorf("visibility private made %v with the body %v", made, patched)
	}
	wantAll("visibility private", out, "visibility: private", "Only people invited: it opens for them and for you", "latere apps share hello <email>...")

	out, stderr, made, err = run("apps", "visibility", "public", "hello")
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
		t.Fatalf("visibility public = %v, want exit 1", err)
	}
	if out != "" || len(made) != 0 {
		t.Errorf("visibility public printed %q and made %v, want neither", out, made)
	}
	wantAll("visibility public stderr", stderr, "an app becomes public when it is shown in the gallery", "reviewed first")

	out, stderr, made, err = run("apps", "share", "hello", "ada@example.com", "me@example.com")
	if err != nil {
		t.Fatalf("share: %v %q", err, stderr)
	}
	if !slices.Equal(made, []string{"GET /v1/apps/apps/hello", "POST /v1/account/apps/" + appID + "/access/invites"}) {
		t.Errorf("share made %v", made)
	}
	wantAll("share", out, "Invited to hello:\n  ada@example.com\n", "me@example.com  your own address", "Invitations are not emailed.", "  https://hello.latere.site\n")
	if strings.Contains(out, "already open") {
		t.Errorf("share of a private app says it is open:\n%s", out)
	}

	out, stderr, _, err = run("apps", "access", "hello")
	if err != nil {
		t.Fatalf("access: %v %q", err, stderr)
	}
	wantAll("access", out, "visibility: private", "EMAIL", "ada@example.com", "pending", "2h")

	out, stderr, made, err = run("apps", "unshare", "hello", "ada@example.com", "nobody@example.com")
	if err != nil {
		t.Fatalf("unshare: %v %q", err, stderr)
	}
	if !slices.Equal(made, []string{"GET /v1/apps/apps/hello", "GET /v1/account/apps/" + appID + "/access", "DELETE /v1/account/apps/" + appID + "/access/inv_0"}) {
		t.Errorf("unshare made %v", made)
	}
	wantAll("unshare", out, "No longer invited to hello:\n  ada@example.com\n", "Not invited to hello, so nothing to take back:\n  nobody@example.com\n")

	out, stderr, _, err = run("apps", "access", "hello")
	if err != nil {
		t.Fatalf("access after the removal: %v %q", err, stderr)
	}
	wantAll("access after the removal", out, "No one is invited.")
}
