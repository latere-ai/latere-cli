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
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// TestAppPasswordE2E drives the built binary through an app's password,
// against a stub of the Apps API with auth's mint and a stub of the platform
// on a server of its own that keeps the password as the platform does: a
// platform that sets none changes nothing, a generated password is printed
// once and makes the app private first, a piped one is printed nowhere, a
// change of visibility removes it, and --remove removes it or says there
// was none. Each API receives the bearer minted for it alone.
func TestAppPasswordE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const (
		appID = "123e4567-e89b-42d3-a456-426614174000"
		piped = "harbor wall at low tide"
	)
	var (
		mu         sync.Mutex
		visibility = "unlisted"
		offered    bool
		password   string
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
			var patched map[string]string
			_ = json.NewDecoder(r.Body).Decode(&patched)
			if len(patched) != 1 || patched["visibility"] == "" {
				t.Errorf("the change of the app carried %d fields, want the visibility alone", len(patched))
			}
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
		unconfigured := func() bool {
			if offered {
				return false
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"passwords_unconfigured","message":"Passwords for apps are not available yet.","details":{"detail":"PLATFORM_APP_PASSWORDS is not on"}}}`)
			return true
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base:
			set, actions := "null", `["invite","revoke"]`
			if offered {
				actions = `["invite","revoke","set_password"]`
				if password != "" {
					set = `{"set_at":"2026-10-08T12:00:00Z"}`
				}
			}
			_, _ = fmt.Fprintf(w, `{"app_id":%q,"people":[],"organization":null,"password":%s,"actions":%s,"limits":{"max_people":50,"max_emails_per_invite":20,"min_password":8,"max_password":128}}`, appID, set, actions)
		case r.Method == http.MethodPut && r.URL.Path == base+"/password":
			if unconfigured() {
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if n := utf8.RuneCountInString(body["password"]); len(body) != 1 || n < 8 || n > 128 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":"invalid_field","message":"The request could not be read.","details":{"field":"password","reason":"length","min":8,"max":128}}}`)
				return
			}
			password = body["password"]
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == base+"/password":
			if unconfigured() {
				return
			}
			password = ""
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
	// run gives the command stdin as its standard input, which is no
	// terminal, and answers stdout, stderr, the error, and the requests the
	// command made at either API in the order they arrived.
	run := func(stdin string, args ...string) (string, string, []string, error) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env, command.Dir = env, work
		command.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		mu.Lock()
		defer mu.Unlock()
		return stdout.String(), stderr.String(), slices.Clone(requests), err
	}
	state := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		return visibility, password
	}
	wantAll := func(what, got string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %q:\n%s", what, want, got)
			}
		}
	}
	exits1 := func(what string, err error) {
		t.Helper()
		if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
			t.Fatalf("%s = %v, want exit 1", what, err)
		}
	}
	const (
		readApp     = "GET /v1/apps/apps/hello"
		changeApp   = "PATCH /v1/apps/apps/hello"
		readAccess  = "GET /v1/account/apps/" + appID + "/access"
		setPassword = "PUT /v1/account/apps/" + appID + "/access/password"
		dropIt      = "DELETE /v1/account/apps/" + appID + "/access/password"
	)
	generated := regexp.MustCompile(`^[abcdefghjkmnpqrstuvwxyz23456789]{5}(-[abcdefghjkmnpqrstuvwxyz23456789]{5}){3}$`)

	// A platform that sets no passwords: the app is not made private for one.
	out, stderr, made, err := run("", "apps", "password", "hello", "--generate")
	exits1("password where the platform sets none", err)
	wantAll("password where the platform sets none, stderr", stderr, "passwords_unconfigured: Passwords are not available on this platform yet. Nothing was changed.")
	if v, p := state(); out != "" || v != "unlisted" || p != "" || !slices.Equal(made, []string{readApp, readAccess}) {
		t.Errorf("password where the platform sets none printed %q, made %v and left the app %s", out, made, v)
	}

	mu.Lock()
	offered = true
	mu.Unlock()

	// Without a terminal and without a flag there is nothing to read a
	// password from, and nothing is asked of either API.
	out, stderr, made, err = run(piped+"\n", "apps", "password", "hello")
	exits1("password without a terminal", err)
	wantAll("password without a terminal, stderr", stderr, "there is no terminal to type the password at", "--stdin", "--generate")
	if out != "" || len(made) != 0 || strings.Contains(stderr, "harbor") {
		t.Errorf("password without a terminal printed %q and %q and made %v", out, stderr, made)
	}

	// A password given as an argument is refused and not repeated.
	out, stderr, made, err = run("", "apps", "password", "hello", "harbor-wall-at-low-tide")
	exits1("password as an argument", err)
	wantAll("password as an argument, stderr", stderr, "a password is never an argument")
	if out != "" || len(made) != 0 || strings.Contains(stderr, "harbor") {
		t.Errorf("password as an argument printed %q and %q and made %v", out, stderr, made)
	}

	// A generated password: the app is made private, then the password is
	// set, and it is printed once, alone on the last line.
	out, stderr, made, err = run("", "apps", "password", "hello", "--generate")
	if err != nil {
		t.Fatalf("password --generate: %v %q", err, stderr)
	}
	v, first := state()
	if v != "private" || !generated.MatchString(first) || !slices.Equal(made, []string{readApp, readAccess, changeApp, setPassword}) {
		t.Errorf("password --generate made %v and left the app %s with a password of %d characters", made, v, len(first))
	}
	wantAll("password --generate", out, "visibility: private", "The password of hello is set.", "Anyone with the password: whoever types it at the app's address opens the app, with no Latere account.",
		"The app was unlisted before and is private now", "The generated password, printed this once:\n"+first+"\n")
	if n := strings.Count(out+stderr, first); n != 1 || !strings.HasSuffix(out, "\n"+first+"\n") {
		t.Errorf("the generated password is printed %d times, want once, on the last line:\n%s", n, out)
	}

	out, stderr, _, err = run("", "apps", "access", "hello")
	if err != nil {
		t.Fatalf("access: %v %q", err, stderr)
	}
	wantAll("access", out, "visibility: private", "Anyone with the password, set 2026-10-08 12:00 UTC: whoever types it at the app's address opens the app")
	out, stderr, _, err = run("", "apps", "access", "hello", "--json")
	if err != nil {
		t.Fatalf("access --json: %v %q", err, stderr)
	}
	var listed struct {
		Access struct {
			Password map[string]string `json:"password"`
		} `json:"access"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil || listed.Access.Password["set_at"] != "2026-10-08T12:00:00Z" {
		t.Errorf("access --json = %q (%v), want the moment the password was set", out, err)
	}

	// A piped password replaces it, and is printed nowhere.
	out, stderr, made, err = run(piped+"\n", "apps", "password", "hello", "--stdin")
	if err != nil {
		t.Fatalf("password --stdin: %v %q", err, stderr)
	}
	if _, p := state(); p != piped || !slices.Equal(made, []string{readApp, readAccess, setPassword}) {
		t.Errorf("password --stdin made %v and set a password of %d characters, want the line piped", made, len(p))
	}
	wantAll("password --stdin", out, "The password of hello is replaced. The one before stops opening the app within a minute.")
	if strings.Contains(out+stderr, "harbor") || strings.Contains(out, "generated") {
		t.Errorf("password --stdin printed the password, or a generated one:\n%s\n%s", out, stderr)
	}

	// A password under the platform's fewest characters changes nothing.
	out, stderr, made, err = run("seven77\n", "apps", "password", "hello", "--stdin")
	exits1("a short password", err)
	wantAll("a short password, stderr", stderr, "a password is at least 8 characters, and this one is shorter. Nothing was changed")
	if _, p := state(); out != "" || p != piped || strings.Contains(stderr, "seven") || !slices.Equal(made, []string{readApp, readAccess}) {
		t.Errorf("a short password printed %q and %q and made %v", out, stderr, made)
	}

	// Anyone with the link: the password goes with the change.
	out, stderr, made, err = run("", "apps", "visibility", "unlisted", "hello")
	if err != nil {
		t.Fatalf("visibility unlisted: %v %q", err, stderr)
	}
	if v, p := state(); v != "unlisted" || p != "" || !slices.Equal(made, []string{readApp, readAccess, changeApp, dropIt}) {
		t.Errorf("visibility unlisted made %v and left the app %s, password set: %t", made, v, p != "")
	}
	wantAll("visibility unlisted", out, "Anyone with the link: it opens without signing in", "Its password was removed. Anyone with the link opens the app")

	// --json carries the generated password once, with the app as the
	// change that made it private answered it.
	out, stderr, _, err = run("", "apps", "password", "hello", "--generate", "--json")
	if err != nil {
		t.Fatalf("password --generate --json: %v %q", err, stderr)
	}
	var set struct {
		App      map[string]any `json:"app"`
		Password struct {
			Set       bool   `json:"set"`
			Generated string `json:"generated"`
		} `json:"password"`
	}
	_, second := state()
	if err := json.Unmarshal([]byte(out), &set); err != nil || !set.Password.Set || set.Password.Generated != second || set.App["visibility"] != "private" {
		t.Errorf("password --generate --json = %q (%v)", out, err)
	}
	if !generated.MatchString(second) || second == first || strings.Count(out+stderr, second) != 1 {
		t.Errorf("the second generated password is the first, misshapen, or not printed once:\n%s", out)
	}

	// Only people invited: the password goes with the change, and the app
	// was private already.
	out, stderr, made, err = run("", "apps", "visibility", "private", "hello")
	if err != nil {
		t.Fatalf("visibility private: %v %q", err, stderr)
	}
	if v, p := state(); v != "private" || p != "" || !slices.Equal(made, []string{readApp, readAccess, changeApp, dropIt}) {
		t.Errorf("visibility private made %v and left the app %s, password set: %t", made, v, p != "")
	}
	wantAll("visibility private", out, "Only people invited: it opens for them and for you", "It was private already.\nIts password was removed. The people who used it no longer get in")

	// --remove of a password that is set, then of none.
	if _, stderr, _, err = run(piped+"\n", "apps", "password", "hello", "--stdin"); err != nil {
		t.Fatalf("password --stdin: %v %q", err, stderr)
	}
	out, stderr, made, err = run("", "apps", "password", "hello", "--remove")
	if err != nil {
		t.Fatalf("password --remove: %v %q", err, stderr)
	}
	if _, p := state(); p != "" || !slices.Equal(made, []string{readApp, readAccess, dropIt}) {
		t.Errorf("password --remove made %v, password set: %t", made, p != "")
	}
	wantAll("password --remove", out, "Removed the password of hello. The people who used it no longer get in, and a visit made with it ends within a minute.",
		"Only people invited: it opens for them and for you")
	out, stderr, made, err = run("", "apps", "password", "hello", "--remove")
	if err != nil {
		t.Fatalf("password --remove of none: %v %q", err, stderr)
	}
	if !slices.Equal(made, []string{readApp, readAccess}) {
		t.Errorf("password --remove of none made %v, want the two reads alone", made)
	}
	wantAll("password --remove of none", out, "hello has no password, so there was none to remove.")

	out, stderr, _, err = run("", "apps", "access", "hello")
	if err != nil {
		t.Fatalf("access after the removal: %v %q", err, stderr)
	}
	wantAll("access after the removal", out, "visibility: private", "Only people invited: it opens for them and for you", "No one is invited.")
	if strings.Contains(out, "password") {
		t.Errorf("access after the removal still says a password:\n%s", out)
	}
}
