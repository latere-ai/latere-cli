package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestGitCredentialGetEmitsSavedToken(t *testing.T) {
	isolateDriveTokens(t)
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))

	out, err := runGitCredential(t, driveGetInput, "get")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "username=token\npassword=access-root\n\n"
	if out != want {
		t.Errorf("get output = %q, want %q", out, want)
	}
}

func TestGitCredentialGetIgnoresOtherHosts(t *testing.T) {
	isolateDriveTokens(t)
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))

	for _, in := range []string{
		"protocol=https\nhost=github.com\n\n",
		"protocol=http\nhost=drive.latere.ai\n\n", // Drive is https-only in prod
	} {
		out, err := runGitCredential(t, in, "get")
		if err != nil {
			t.Fatalf("get(%q): %v", in, err)
		}
		if out != "" {
			t.Errorf("get(%q) output = %q, want empty (not our host)", in, out)
		}
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

	// The dev override may be plain http.
	out, err := runGitCredential(t, "protocol=http\nhost=localhost:8080\n\n", "get")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(out, "password=access-root") {
		t.Errorf("get output = %q, want the saved token for the DRIVE_HOST override", out)
	}

	// The override replaces the production host, it does not add to it.
	out, err = runGitCredential(t, driveGetInput, "get")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out != "" {
		t.Errorf("get output = %q, want empty for drive.latere.ai while DRIVE_HOST=localhost:8080", out)
	}
}

func TestGitCredentialGetRefreshesExpiredToken(t *testing.T) {
	isolateDriveTokens(t)
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
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
	}))
	defer authSrv.Close()
	writeAuthTokenFile(t, "access-old", "refresh-old", time.Now().Add(-time.Hour))

	out, err := runGitCredential(t, driveGetInput, "get", "--auth-url", authSrv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "username=token\npassword=access-new\n\n"
	if out != want {
		t.Errorf("get output = %q, want the refreshed token %q", out, want)
	}
}

func TestGitCredentialGetFallsBackToCellaToken(t *testing.T) {
	isolateDriveTokens(t)
	// No auth-token.json (a --token paste login clears it); token.json holds
	// the pasted bearer.
	p := filepath.Join(t.TempDir(), "token.json")
	b, _ := json.Marshal(map[string]any{"access_token": "pasted-token", "token_type": "Bearer"})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_TOKEN_FILE", p)

	out, err := runGitCredential(t, driveGetInput, "get")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "username=token\npassword=pasted-token\n\n"
	if out != want {
		t.Errorf("get output = %q, want %q", out, want)
	}
}

func TestGitCredentialStoreEraseAreNoops(t *testing.T) {
	isolateDriveTokens(t)
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))

	for _, op := range []string{"store", "erase"} {
		out, err := runGitCredential(t, "protocol=https\nhost=drive.latere.ai\nusername=token\npassword=whatever\n\n", op)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if out != "" {
			t.Errorf("%s output = %q, want empty (no-op)", op, out)
		}
	}
	// The auth token file must be untouched by erase.
	if tok, ok := driveCredentialToken(t.Context(), ""); !ok || tok != "access-root" {
		t.Errorf("token after erase = (%q, %v), want the saved login intact", tok, ok)
	}
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
