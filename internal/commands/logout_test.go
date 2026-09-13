// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

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

// seedLogin isolates the one credential file and seeds a login carrying a
// refresh token, which is what logout revokes.
func seedLogin(t *testing.T) (authPath string) {
	t.Helper()
	authPath = filepath.Join(t.TempDir(), "auth-token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
	t.Setenv("AUTH_URL", "")
	if err := api.SaveAuthToken(api.Token{
		AccessToken:  "login-access",
		RefreshToken: "login-refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return authPath
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

// Logout speaks to the issuer and to nobody else. There is no product-held
// credential to recall: an actor token lapses within five minutes.
func TestLogoutRevokesAtTheIssuerAndClearsTheLogin(t *testing.T) {
	var revokes atomic.Int32
	var gotForm atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		revokes.Add(1)
		_ = r.ParseForm()
		gotForm.Store(r.PostForm.Encode())
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("logout called %s; it speaks to the issuer alone", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	authPath := seedLogin(t)

	errOut := runLogout(t, "--auth-url", srv.URL)

	if revokes.Load() != 1 {
		t.Errorf("revoke calls = %d, want 1", revokes.Load())
	}
	form, _ := gotForm.Load().(string)
	for _, want := range []string{"token=login-refresh", "token_type_hint=refresh_token", "client_id=latere-cli"} {
		if !strings.Contains(form, want) {
			t.Errorf("revoke form = %q, missing %q", form, want)
		}
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Errorf("%s still exists after logout", authPath)
	}
	if !strings.Contains(errOut, "Logged out.") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLogoutSucceedsWhenTheIssuerIsDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	deadURL := srv.URL
	srv.Close() // connection refused from here on
	authPath := seedLogin(t)

	errOut := runLogout(t, "--auth-url", deadURL)

	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Errorf("%s still exists; logout must clear locally even offline", authPath)
	}
	if !strings.Contains(errOut, "warning: could not revoke the refresh token") {
		t.Errorf("stderr should warn about the failed revocation, got %q", errOut)
	}
	if !strings.Contains(errOut, "Logged out.") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLogoutSkipsRevocationWithNothingSaved(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	errOut := runLogout(t) // no file, no server: nothing to revoke, still succeeds
	if !strings.Contains(errOut, "Logged out.") {
		t.Errorf("stderr = %q", errOut)
	}
	if strings.Contains(errOut, "warning") {
		t.Errorf("no saved login must produce no warnings, got %q", errOut)
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

func TestLogoutRevocationDegrades(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	seedLogin(t)

	// The auth base resolves through AUTH_URL when no flag is given.
	t.Setenv("AUTH_URL", srv.URL)
	errOut := runLogout(t)
	if !strings.Contains(errOut, "revocation returned 500") {
		t.Errorf("stderr = %q, want the 500 warning", errOut)
	}
}

func TestLogoutBadAuthURLWarnsAndCompletes(t *testing.T) {
	authPath := seedLogin(t)

	errOut := runLogout(t, "--auth-url", "http://[bad")
	if !strings.Contains(errOut, "could not revoke the refresh token") {
		t.Errorf("stderr = %q, want the request-build warning", errOut)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Error("the login must be cleared regardless")
	}
}

func TestLogoutSurfacesFileClearErrors(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(locked, "auth-token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
	if err := api.SaveAuthToken(api.Token{AccessToken: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil { // parent unwritable: Remove fails
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	root := NewRoot("test")
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	root.SetArgs([]string{"logout", "--auth-url", server.URL})
	if err := root.Execute(); err == nil {
		t.Fatal("logout with an undeletable token file: want error")
	}
}
