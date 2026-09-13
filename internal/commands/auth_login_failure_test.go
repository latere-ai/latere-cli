// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

// A pasted token is verified at the issuer, which is who addresses it. A
// rejected one must not replace the login already on file.
func TestPastedLoginVerifiesAtTheIssuer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "auth-token.json"))
	if err := api.SaveAuthToken(api.Token{AccessToken: "old-login"}); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokeninfo" {
			t.Errorf("login called %s; it speaks to the issuer alone", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		probes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer good-candidate" {
			_, _ = w.Write([]byte(`{"sub":"u-1"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"invalid_token"}`))
	}))
	defer server.Close()

	if err := loginWithPastedToken(t.Context(), server.URL, "rejected-candidate"); err == nil {
		t.Error("a token the issuer refused was accepted as a login")
	}
	if got, err := api.LoadAuthToken(); err != nil || got.AccessToken != "old-login" {
		t.Errorf("failed verification changed the saved login: %q (%v)", got.AccessToken, err)
	}
	if err := loginWithPastedToken(t.Context(), server.URL, "good-candidate"); err != nil {
		t.Fatalf("accepted token rejected: %v", err)
	}
	if got, err := api.LoadAuthToken(); err != nil || got.AccessToken != "good-candidate" {
		t.Errorf("saved login = %q (%v), want the pasted token", got.AccessToken, err)
	}
	if probes.Load() != 2 {
		t.Errorf("issuer probes = %d, want one per login", probes.Load())
	}
}

// The login token is the only credential written. A pasted login must not
// leave a second file beside it.
func TestPastedLoginWritesOneCredential(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "auth-token.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"u-1"}`))
	}))
	defer server.Close()

	if err := loginWithPastedToken(t.Context(), server.URL, "candidate"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth-token.json" {
		t.Fatalf("login wrote %v, want auth-token.json alone", entries)
	}
}

func TestLoginCancellationPreservesSavedToken(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "auth-token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", path)
	before := `{"access_token":"old-login"}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := loginWithPastedToken(ctx, "http://127.0.0.1:1", "candidate"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context cancellation", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != before {
		t.Errorf("cancelled login changed the saved login: %v", err)
	}
}
