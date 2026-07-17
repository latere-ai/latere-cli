package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

// seedLogoutFiles isolates both token files and seeds them with a cella
// bearer and an auth root token carrying a refresh token.
func seedLogoutFiles(t *testing.T) (tokenPath, authPath string) {
	t.Helper()
	dir := t.TempDir()
	tokenPath = filepath.Join(dir, "token.json")
	authPath = filepath.Join(dir, "auth-token.json")
	t.Setenv("LATERE_TOKEN_FILE", tokenPath)
	t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
	if err := api.SaveToken("", api.Token{AccessToken: "cella-tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if err := api.SaveAuthToken(api.Token{
		AccessToken:  "root-access",
		RefreshToken: "root-refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return tokenPath, authPath
}

func runLogout(t *testing.T, args ...string) string {
	t.Helper()
	root := NewRoot("test")
	var errBuf bytes.Buffer
	root.SetOut(new(bytes.Buffer))
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"logout"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("logout: %v (stderr: %s)", err, errBuf.String())
	}
	return errBuf.String()
}

func TestLogoutRevokesBothSidesAndClearsFiles(t *testing.T) {
	var cellaRevokes, authRevokes atomic.Int32
	var gotBearer, gotForm atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/tokens/current", func(w http.ResponseWriter, r *http.Request) {
		cellaRevokes.Add(1)
		gotBearer.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		authRevokes.Add(1)
		_ = r.ParseForm()
		gotForm.Store(r.PostForm.Encode())
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tokenPath, authPath := seedLogoutFiles(t)

	errOut := runLogout(t, "--api-url", srv.URL, "--auth-url", srv.URL)

	if cellaRevokes.Load() != 1 || authRevokes.Load() != 1 {
		t.Errorf("revoke calls cella=%d auth=%d, want 1 and 1", cellaRevokes.Load(), authRevokes.Load())
	}
	if got, _ := gotBearer.Load().(string); got != "Bearer cella-tok" {
		t.Errorf("cella revoke bearer = %q", got)
	}
	form, _ := gotForm.Load().(string)
	for _, want := range []string{"token=root-refresh", "token_type_hint=refresh_token", "client_id=latere-cli"} {
		if !strings.Contains(form, want) {
			t.Errorf("revoke form = %q, missing %q", form, want)
		}
	}
	for _, p := range []string{tokenPath, authPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after logout", p)
		}
	}
	if !strings.Contains(errOut, "Logged out.") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLogoutSucceedsWhenServersAreDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	deadURL := srv.URL
	srv.Close() // connection refused from here on
	tokenPath, authPath := seedLogoutFiles(t)

	errOut := runLogout(t, "--api-url", deadURL, "--auth-url", deadURL)

	for _, p := range []string{tokenPath, authPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists; logout must clear locally even offline", p)
		}
	}
	if !strings.Contains(errOut, "warning: could not revoke the cella token") ||
		!strings.Contains(errOut, "warning: could not revoke the auth refresh token") {
		t.Errorf("stderr should warn about both failed revocations, got %q", errOut)
	}
	if !strings.Contains(errOut, "Logged out.") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLogoutOlderServerDegradesToNote(t *testing.T) {
	mux := http.NewServeMux() // no /v1/tokens/current route -> 404
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	seedLogoutFiles(t)

	errOut := runLogout(t, "--api-url", srv.URL, "--auth-url", srv.URL)
	if !strings.Contains(errOut, "server-side token revocation unavailable (404)") {
		t.Errorf("stderr = %q, want the 404 degradation note", errOut)
	}
}

// TestLoginAndRefreshShareOneScopeSet pins the single scope-set
// definition: the login flag default must be exactly api.LoginScopes,
// the same constant the refresh path's oidc config is built from.
func TestLoginAndRefreshShareOneScopeSet(t *testing.T) {
	f := newAuthLoginCmd().Flags().Lookup("scopes")
	if f == nil {
		t.Fatal("login has no --scopes flag")
	}
	if f.DefValue != api.LoginScopes {
		t.Errorf("login --scopes default = %q, want api.LoginScopes %q", f.DefValue, api.LoginScopes)
	}
}

func TestLogoutAuthRevocationDegrades(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/tokens/current", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	seedLogoutFiles(t)

	// The auth base resolves through AUTH_URL when no flag is given.
	t.Setenv("AUTH_URL", srv.URL)
	errOut := runLogout(t, "--api-url", srv.URL)
	if !strings.Contains(errOut, "revocation returned 500") {
		t.Errorf("stderr = %q, want the 500 warning", errOut)
	}
}

func TestLogoutBadAuthURLWarnsAndCompletes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/tokens/current", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tokenPath, _ := seedLogoutFiles(t)

	errOut := runLogout(t, "--api-url", srv.URL, "--auth-url", "http://[bad")
	if !strings.Contains(errOut, "could not revoke the auth refresh token") {
		t.Errorf("stderr = %q, want the request-build warning", errOut)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Error("token.json must be cleared regardless")
	}
}

func TestLogoutSkipsRevocationWithNothingSaved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(dir, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))
	errOut := runLogout(t) // no files, no servers: nothing to revoke, still succeeds
	if !strings.Contains(errOut, "Logged out.") {
		t.Errorf("stderr = %q", errOut)
	}
	if strings.Contains(errOut, "warning") {
		t.Errorf("no saved tokens must produce no warnings, got %q", errOut)
	}
}

func TestLogoutSurfacesFileClearErrors(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(locked, "token.json")
	t.Setenv("LATERE_TOKEN_FILE", tokenPath)
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil { // parent unwritable: Remove fails
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	root := NewRoot("test")
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"logout"})
	if err := root.Execute(); err == nil {
		t.Fatal("logout with an undeletable token file: want error")
	}
}
