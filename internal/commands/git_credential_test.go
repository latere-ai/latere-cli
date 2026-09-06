// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

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

// driveAuthStub is a fake auth service for the git helper: /token refreshes
// the root token and /actor-tokens mints the Drive credential. It records the
// mint request so a test can pin the bearer, audience and TTL the helper
// sends. mintStatus, when nonzero, makes every mint fail with that status.
type driveAuthStub struct {
	srv        *httptest.Server
	mintStatus int

	mu           sync.Mutex
	refreshes    int
	mints        int
	mintBearer   string
	mintAudience string
	mintTTL      float64
}

func newDriveAuthStub(t *testing.T) *driveAuthStub {
	t.Helper()
	s := &driveAuthStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/token":
			s.refreshes++
			_ = r.ParseForm()
			if r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "refresh-old" {
				http.Error(w, "bad grant", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-new", "token_type": "Bearer",
				"refresh_token": "refresh-new", "expires_in": 3600,
			})
		case "/actor-tokens":
			s.mints++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mintBearer = r.Header.Get("Authorization")
			s.mintAudience, _ = body["audience"].(string)
			s.mintTTL, _ = body["ttl_seconds"].(float64)
			if s.mintStatus != 0 {
				http.Error(w, "mint failed", s.mintStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"actor_token": "drive-actor", "expires_in": 300})
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// assertDriveMint checks the single mint the helper is expected to make:
// presenting bearer, for the Drive audience, with the fixed 300s TTL.
func (s *driveAuthStub) assertDriveMint(t *testing.T, bearer string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mints != 1 {
		t.Errorf("mints = %d, want 1", s.mints)
	}
	if s.mintBearer != "Bearer "+bearer {
		t.Errorf("mint bearer = %q, want %q", s.mintBearer, "Bearer "+bearer)
	}
	if s.mintAudience != "drive.latere.ai" {
		t.Errorf("mint audience = %q, want drive.latere.ai", s.mintAudience)
	}
	if s.mintTTL != 300 {
		t.Errorf("mint ttl_seconds = %v, want 300", s.mintTTL)
	}
}

func (s *driveAuthStub) counts() (refreshes, mints int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes, s.mints
}

const driveActorOutput = "username=token\npassword=drive-actor\n\n"

// isolateDriveTokens points both token files at absent paths so a
// developer's real ~/.config/latere login never leaks into a test, and
// clears any DRIVE_HOST override from the environment.
func isolateDriveTokens(t *testing.T) {
	t.Helper()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(t.TempDir(), "absent-token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "absent-auth-token.json"))
	t.Setenv("DRIVE_HOST", "")
}

// runGitCredential executes `latere git-credential <args>` with in on stdin
// and returns captured stdout.
func runGitCredential(t *testing.T, in string, args ...string) (string, error) {
	t.Helper()
	cmd := newGitCredentialCmd()
	cmd.SetIn(strings.NewReader(in))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

const driveGetInput = "protocol=https\nhost=drive.latere.ai\npath=git/me/notes.git\n\n"

// The helper never hands git the root token: it mints an actor token bound
// to Drive's audience with the root token as bearer, and emits that.
func TestGitCredentialGetMintsDriveActorToken(t *testing.T) {
	isolateDriveTokens(t)
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))
	auth := newDriveAuthStub(t)

	out, err := runGitCredential(t, driveGetInput, "get", "--auth-url", auth.srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out != driveActorOutput {
		t.Errorf("get output = %q, want the minted Drive token %q", out, driveActorOutput)
	}
	if strings.Contains(out, "access-root") {
		t.Errorf("get output = %q leaks the root token to git", out)
	}
	auth.assertDriveMint(t, "access-root")
	if refreshes, _ := auth.counts(); refreshes != 0 {
		t.Errorf("refreshes = %d, want 0 for a fresh root token", refreshes)
	}
}

// A mint that fails, whether auth rejects it or is unreachable, is the
// same outcome as a failed refresh: nothing printed, exit 0, git prompts.
func TestGitCredentialGetSilentWhenMintFails(t *testing.T) {
	t.Run("auth rejects", func(t *testing.T) {
		isolateDriveTokens(t)
		writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))
		auth := newDriveAuthStub(t)
		auth.mintStatus = http.StatusInternalServerError

		out, err := runGitCredential(t, driveGetInput, "get", "--auth-url", auth.srv.URL)
		if err != nil {
			t.Fatalf("get must exit 0 when the mint fails, got %v", err)
		}
		if out != "" {
			t.Errorf("get output = %q, want empty when the mint fails", out)
		}
	})
	t.Run("auth unreachable", func(t *testing.T) {
		isolateDriveTokens(t)
		writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))
		auth := newDriveAuthStub(t)
		url := auth.srv.URL
		auth.srv.Close()

		out, err := runGitCredential(t, driveGetInput, "get", "--auth-url", url)
		if err != nil {
			t.Fatalf("get must exit 0 when auth is unreachable, got %v", err)
		}
		if out != "" {
			t.Errorf("get output = %q, want empty when auth is unreachable", out)
		}
	})
}

func TestGitCredentialGetIgnoresOtherHosts(t *testing.T) {
	isolateDriveTokens(t)
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))
	auth := newDriveAuthStub(t)

	for _, in := range []string{
		"protocol=https\nhost=github.com\n\n",
		"protocol=http\nhost=drive.latere.ai\n\n", // Drive is https-only in prod
	} {
		out, err := runGitCredential(t, in, "get", "--auth-url", auth.srv.URL)
		if err != nil {
			t.Fatalf("get(%q): %v", in, err)
		}
		if out != "" {
			t.Errorf("get(%q) output = %q, want empty (not our host)", in, out)
		}
	}
	if refreshes, mints := auth.counts(); refreshes != 0 || mints != 0 {
		t.Errorf("auth calls = %d refreshes, %d mints; want none for a foreign host", refreshes, mints)
	}
}

func TestGitCredentialGetSilentWhenLoggedOut(t *testing.T) {
	isolateDriveTokens(t)

	out, err := runGitCredential(t, driveGetInput, "get")
	if err != nil {
		t.Fatalf("get must exit 0 without a login (git falls back to prompting), got %v", err)
	}
	if out != "" {
		t.Errorf("get output = %q, want empty when not logged in", out)
	}
}

func TestGitCredentialGetHonorsDriveHostOverride(t *testing.T) {
	isolateDriveTokens(t)
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))
	t.Setenv("DRIVE_HOST", "localhost:8080")
	auth := newDriveAuthStub(t)

	// The dev override may be plain http. The audience stays the production
	// one: DRIVE_HOST picks the git host, not the aud claim.
	out, err := runGitCredential(t, "protocol=http\nhost=localhost:8080\n\n", "get", "--auth-url", auth.srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out != driveActorOutput {
		t.Errorf("get output = %q, want the minted token for the DRIVE_HOST override", out)
	}
	auth.assertDriveMint(t, "access-root")

	// The override replaces the production host, it does not add to it.
	out, err = runGitCredential(t, driveGetInput, "get", "--auth-url", auth.srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out != "" {
		t.Errorf("get output = %q, want empty for drive.latere.ai while DRIVE_HOST=localhost:8080", out)
	}
}

// An expired root token is refreshed first; the refreshed value is the
// bearer for the mint, and git still only sees the minted token.
func TestGitCredentialGetRefreshesExpiredToken(t *testing.T) {
	isolateDriveTokens(t)
	auth := newDriveAuthStub(t)
	writeAuthTokenFile(t, "access-old", "refresh-old", time.Now().Add(-time.Hour))

	out, err := runGitCredential(t, driveGetInput, "get", "--auth-url", auth.srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out != driveActorOutput {
		t.Errorf("get output = %q, want the minted token %q", out, driveActorOutput)
	}
	if refreshes, _ := auth.counts(); refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes)
	}
	auth.assertDriveMint(t, "access-new")
}

// A --token paste login has no auth file, so there is no root token to
// mint from; the pasted bearer is presented verbatim and auth is not called.
func TestGitCredentialGetFallsBackToCellaToken(t *testing.T) {
	isolateDriveTokens(t)
	auth := newDriveAuthStub(t)
	// No auth-token.json (a --token paste login clears it); token.json holds
	// the pasted bearer.
	p := filepath.Join(t.TempDir(), "token.json")
	b, _ := json.Marshal(map[string]any{"access_token": "pasted-token", "token_type": "Bearer"})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_TOKEN_FILE", p)

	out, err := runGitCredential(t, driveGetInput, "get", "--auth-url", auth.srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "username=token\npassword=pasted-token\n\n"
	if out != want {
		t.Errorf("get output = %q, want %q", out, want)
	}
	if refreshes, mints := auth.counts(); refreshes != 0 || mints != 0 {
		t.Errorf("auth calls = %d refreshes, %d mints; want none for a pasted token", refreshes, mints)
	}
}

func TestGitCredentialStoreEraseAreNoops(t *testing.T) {
	isolateDriveTokens(t)
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))
	auth := newDriveAuthStub(t)

	for _, op := range []string{"store", "erase"} {
		out, err := runGitCredential(t, "protocol=https\nhost=drive.latere.ai\nusername=token\npassword=whatever\n\n", op)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if out != "" {
			t.Errorf("%s output = %q, want empty (no-op)", op, out)
		}
	}
	// The auth token file must be untouched by erase: the saved login still
	// mints.
	if tok, err := driveCredentialToken(t.Context(), auth.srv.URL); err != nil || tok != "drive-actor" {
		t.Errorf("token after erase = (%q, %v), want a mint from the intact login", tok, err)
	}
	auth.assertDriveMint(t, "access-root")
}

func TestParseCredentialAttrs(t *testing.T) {
	in := "protocol=https\nhost=drive.latere.ai\nurl=https://x@drive.latere.ai/a=b\nmalformed line\n\nignored=after-blank\n"
	attrs, err := parseCredentialAttrs(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCredentialAttrs: %v", err)
	}
	want := map[string]string{
		"protocol": "https",
		"host":     "drive.latere.ai",
		"url":      "https://x@drive.latere.ai/a=b", // values may contain '='
	}
	if len(attrs) != len(want) {
		t.Errorf("attrs = %v, want %v (stop at blank line, skip malformed)", attrs, want)
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("attrs[%q] = %q, want %q", k, attrs[k], v)
		}
	}
}

// setupGitConfigFile points git's global config at a scratch file (git
// honors GIT_CONFIG_GLOBAL) and returns a reader for the configured helper
// values. Skips when git is not installed.
func setupGitConfigFile(t *testing.T) func() []string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	key := "credential.https://drive.latere.ai.helper"
	return func() []string {
		out, err := exec.Command("git", "config", "--global", "--get-all", key).Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == 1 { // key not set
				return nil
			}
			t.Fatalf("git config --get-all: %v", err)
		}
		vals := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		return vals
	}
}

func TestGitCredentialSetupWritesScopedHelper(t *testing.T) {
	isolateDriveTokens(t)
	getAll := setupGitConfigFile(t)

	if _, err := runGitCredential(t, "", "setup"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	want := []string{"", "!latere git-credential"} // empty reset entry first
	got := getAll()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("helper entries = %q, want %q", got, want)
	}

	// Re-running setup is idempotent: still exactly the same two entries.
	if _, err := runGitCredential(t, "", "setup"); err != nil {
		t.Fatalf("setup (rerun): %v", err)
	}
	got = getAll()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("helper entries after rerun = %q, want %q", got, want)
	}
}

func TestGitCredentialSetupRemove(t *testing.T) {
	isolateDriveTokens(t)
	getAll := setupGitConfigFile(t)

	if _, err := runGitCredential(t, "", "setup"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := runGitCredential(t, "", "setup", "--remove"); err != nil {
		t.Fatalf("setup --remove: %v", err)
	}
	if got := getAll(); len(got) != 0 {
		t.Fatalf("helper entries after --remove = %q, want none", got)
	}

	// Removing when nothing is configured is not an error.
	if _, err := runGitCredential(t, "", "setup", "--remove"); err != nil {
		t.Fatalf("setup --remove (already removed): %v", err)
	}
}

// fakeSandboxAPI serves the /v1/sandboxes probe saveAndVerify uses to
// confirm a pasted token.
func fakeSandboxAPI(t *testing.T, acceptToken bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sandboxes" {
			http.NotFound(w, r)
			return
		}
		if !acceptToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// swapGitWiring replaces the post-login git wiring seam with a counter.
func swapGitWiring(t *testing.T) *int {
	t.Helper()
	orig := configureDriveGitAfterLogin
	t.Cleanup(func() { configureDriveGitAfterLogin = orig })
	calls := 0
	configureDriveGitAfterLogin = func(ctx context.Context, errw io.Writer) { calls++ }
	return &calls
}

func runAuthLogin(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newAuthLoginCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	_, err := captureStdout(func() error { return cmd.Execute() })
	return err
}

func TestAuthLoginWiresGitHelperOnce(t *testing.T) {
	isolateDriveTokens(t)
	calls := swapGitWiring(t)
	srv := fakeSandboxAPI(t, true)

	if err := runAuthLogin(t, "--token", "pasted-token", "--api-url", srv.URL); err != nil {
		t.Fatalf("login --token: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("git wiring invoked %d times, want exactly 1", *calls)
	}
}

func TestAuthLoginNoGitSkipsWiring(t *testing.T) {
	isolateDriveTokens(t)
	calls := swapGitWiring(t)
	srv := fakeSandboxAPI(t, true)

	if err := runAuthLogin(t, "--token", "pasted-token", "--api-url", srv.URL, "--no-git"); err != nil {
		t.Fatalf("login --token --no-git: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("git wiring invoked %d times with --no-git, want 0", *calls)
	}
}

func TestAuthLoginFailureSkipsWiring(t *testing.T) {
	isolateDriveTokens(t)
	calls := swapGitWiring(t)
	srv := fakeSandboxAPI(t, false)

	if err := runAuthLogin(t, "--token", "bad-token", "--api-url", srv.URL); err == nil {
		t.Fatal("login with rejected token succeeded, want error")
	}
	if *calls != 0 {
		t.Fatalf("git wiring invoked %d times after failed login, want 0", *calls)
	}
}

func TestAutoConfigureDriveGitIdempotent(t *testing.T) {
	isolateDriveTokens(t)
	getAll := setupGitConfigFile(t)

	var errw bytes.Buffer
	autoConfigureDriveGit(t.Context(), &errw)
	autoConfigureDriveGit(t.Context(), &errw) // second run: already configured, skip the write

	want := []string{"", "!latere git-credential"}
	got := getAll()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("helper entries = %q, want %q", got, want)
	}
	announced := strings.Count(errw.String(), "git is configured for drive.latere.ai")
	if announced != 2 {
		t.Errorf("announcement printed %d times, want 2 (once per login):\n%s", announced, errw.String())
	}
	if strings.Contains(errw.String(), "warning") {
		t.Errorf("unexpected warning: %s", errw.String())
	}
}

func TestAuthLoginSucceedsWithoutGitBinary(t *testing.T) {
	isolateDriveTokens(t)
	srv := fakeSandboxAPI(t, true)
	// An empty PATH hides git; login must still succeed and the real
	// wiring hook must skip silently.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))

	cmd := newAuthLoginCmd()
	var errb bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"--token", "pasted-token", "--api-url", srv.URL})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("login without git on PATH: %v", err)
	}
	if s := errb.String(); strings.Contains(s, "git is configured") || strings.Contains(s, "warning") {
		t.Errorf("expected a silent skip without git, got: %s", s)
	}
}

func TestSkipUpdateCheckForGitCredential(t *testing.T) {
	root := NewRoot("test")
	cmd, _, err := root.Find([]string{"git-credential", "get"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !skipUpdateCheck(cmd) {
		t.Error("skipUpdateCheck(git-credential get) = false, want true (git parses helper stdout; a notice or auto-upgrade must not interleave)")
	}
}

func TestAutoGitSetupRepairsMissingDevelopmentHTTPHelper(t *testing.T) {
	isolateDriveTokens(t)
	setupGitConfigFile(t)
	t.Setenv("DRIVE_HOST", "localhost:8080")
	key := "credential.https://localhost:8080.helper"
	if err := gitConfig(t.Context(), "--replace-all", key, ""); err != nil {
		t.Fatal(err)
	}
	if err := gitConfig(t.Context(), "--add", key, "!latere git-credential"); err != nil {
		t.Fatal(err)
	}
	if driveGitHelperConfigured(t.Context()) {
		t.Error("HTTPS-only setup incorrectly considered complete for a development host")
	}
	autoConfigureDriveGit(t.Context(), io.Discard)
	out, err := exec.Command("git", "config", "--global", "--get-all", "credential.http://localhost:8080.helper").Output()
	if err != nil || string(out) != "\n!latere git-credential\n" {
		t.Errorf("automatic setup did not repair HTTP helpers: %q (%v)", out, err)
	}
	if !driveGitHelperConfigured(t.Context()) {
		t.Error("repaired setup not recognized")
	}
}
