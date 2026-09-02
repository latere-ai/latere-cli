// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshAuthTokenPersistsAndPreservesRefreshToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q", got)
		}
		// Omit refresh_token in the reply: the previous one must be kept.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access", "token_type": "Bearer", "expires_in": 3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := RefreshAuthToken(context.Background(), srv.URL, "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "old-refresh" {
		t.Errorf("refreshed = %+v; want new-access with preserved old-refresh", got)
	}
	saved, err := LoadAuthToken()
	if err != nil || saved.AccessToken != "new-access" {
		t.Errorf("persisted auth token = %+v, %v", saved, err)
	}
}

func TestRefreshCellaTokenRefreshesExpiringRoot(t *testing.T) {
	f := &fakePlane{t: t}
	mux := http.NewServeMux()
	mux.Handle("/", f.handler())
	refreshed := false
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		refreshed = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh-root", "token_type": "Bearer", "expires_in": 3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(dir, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))
	if err := SaveAuthToken(Token{
		AccessToken:  "stale-root",
		RefreshToken: "root-refresh",
		ExpiresAt:    time.Now().Add(-time.Minute), // already expired
	}); err != nil {
		t.Fatal(err)
	}

	tok, ok := RefreshCellaToken(context.Background(), srv.URL)
	if !ok || tok != "fresh-cella-tok" {
		t.Fatalf("RefreshCellaToken = %q, %v", tok, ok)
	}
	if !refreshed {
		t.Error("expiring root token was not refreshed first")
	}
}

func TestRefreshCellaTokenFailurePaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(dir, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))

	// No auth root token at all.
	if _, ok := RefreshCellaToken(context.Background(), "http://127.0.0.1:0"); ok {
		t.Error("want ok=false without an auth root token")
	}

	// Hard mint failure (server returns 500 on /actor-tokens).
	mux := http.NewServeMux()
	mux.HandleFunc("POST /actor-tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	if err := SaveAuthToken(Token{AccessToken: "root", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := RefreshCellaToken(context.Background(), srv.URL); ok {
		t.Error("want ok=false on actor-token mint failure")
	}

	// Root refresh fails for an expired root.
	if err := SaveAuthToken(Token{
		AccessToken: "root", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := RefreshCellaToken(context.Background(), srv.URL); ok {
		t.Error("want ok=false when the root refresh fails")
	}

	// Exchange failure after a good mint.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("POST /actor-tokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"actor_token": "a"})
	})
	mux2.HandleFunc("POST /v1/tokens/exchange", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	t.Setenv("AUTH_URL", srv2.URL)
	if err := SaveAuthToken(Token{AccessToken: "root", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := RefreshCellaToken(context.Background(), srv2.URL); ok {
		t.Error("want ok=false on exchange failure")
	}
}

func TestMintActorTokenErrorShapes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /actor-tokens", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer legacy":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("audience mismatch"))
		case "Bearer empty":
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("nope"))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	httpc := srv.Client()

	if _, err := MintActorToken(context.Background(), httpc, srv.URL, "legacy", "sandboxd", 60); !errors.Is(err, ErrActorAudienceMismatch) {
		t.Errorf("legacy shape: err = %v, want ErrActorAudienceMismatch", err)
	}
	if _, err := MintActorToken(context.Background(), httpc, srv.URL, "empty", "sandboxd", 60); err == nil {
		t.Error("empty response: want error")
	}
	if _, err := MintActorToken(context.Background(), httpc, srv.URL, "other", "sandboxd", 60); err == nil {
		t.Error("non-2xx: want error")
	}
}

func TestExchangeAtCellaErrorShapes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tokens/exchange", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer empty":
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("no"))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := ExchangeAtCella(context.Background(), srv.Client(), srv.URL, "empty"); err == nil {
		t.Error("empty response: want error")
	}
	if _, err := ExchangeAtCella(context.Background(), srv.Client(), srv.URL, "other"); err == nil {
		t.Error("non-2xx: want error")
	}
}

func TestNewClientResolvesBaseFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(dir, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))

	t.Setenv("SANDBOX_API_URL", "https://cella.example/")
	if c := NewClient(""); c.BaseURL != "https://cella.example" {
		t.Errorf("BaseURL = %q, want env value trimmed", c.BaseURL)
	}
	t.Setenv("SANDBOX_API_URL", "")
	if c := NewClient(""); c.BaseURL != DefaultAPIURL {
		t.Errorf("BaseURL = %q, want default", c.BaseURL)
	}
	if c := NewClient("https://x.example"); c.Refresh == nil {
		t.Error("NewClient must wire the default Refresh hook")
	}
}

func TestClientRetriesSeekableBodyAfterRefresh(t *testing.T) {
	f := &fakePlane{t: t}
	f.acceptBearer.Store("fresh-cella-tok")
	mux := http.NewServeMux()
	mux.Handle("/", f.handler())
	var postCalls int32
	mux.HandleFunc("POST /v1/things", func(w http.ResponseWriter, r *http.Request) {
		postCalls++
		if r.Header.Get("Authorization") != "Bearer fresh-cella-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized","message":"stale"}`))
			return
		}
		body, _ := readAllString(r)
		if body != `{"x":1}` {
			t.Errorf("retried body = %q, want the rewound original", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	seedTokens(t, Token{AccessToken: "stale-tok", TokenType: "Bearer"}, "root-access")

	c := NewClient(srv.URL)
	if err := c.PostJSON(context.Background(), "/v1/things", map[string]int{"x": 1}, nil); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if postCalls != 2 {
		t.Errorf("post calls = %d, want 2 (401 then rewound retry)", postCalls)
	}
}

func readAllString(r *http.Request) (string, error) {
	b := make([]byte, 0, 64)
	buf := make([]byte, 64)
	for {
		n, err := r.Body.Read(buf)
		b = append(b, buf[:n]...)
		if errors.Is(err, io.EOF) {
			return string(b), nil
		}
		if err != nil {
			return string(b), err
		}
	}
}

func TestExchangeHelpersRejectUnbuildableRequests(t *testing.T) {
	if _, err := ExchangeAtCella(context.Background(), http.DefaultClient, "http://[bad", "b"); err == nil {
		t.Error("bad base URL: want request-build error")
	}
	if _, err := MintActorToken(context.Background(), http.DefaultClient, "http://[bad", "b", "sandboxd", 60); err == nil {
		t.Error("bad auth URL: want request-build error")
	}
}

func TestRefreshAuthTokenErrorPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))

	if _, err := RefreshAuthToken(context.Background(), "", "r"); err == nil {
		t.Error("empty auth base: want error")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := RefreshAuthToken(context.Background(), srv.URL, "r"); err == nil {
		t.Error("500 from /token: want error")
	}
}
