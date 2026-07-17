package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakePlane serves both the auth endpoints (/actor-tokens) and the
// cella endpoints (/v1/tokens/exchange, /v1/sandboxes) from one server
// so AUTH_URL and the API base can point at the same host.
type fakePlane struct {
	t             *testing.T
	acceptBearer  atomic.Value // string: the bearer /v1/sandboxes accepts
	actorRejects  bool         // /actor-tokens returns the audience-mismatch shape
	sandboxCalls  atomic.Int32
	exchangeCalls atomic.Int32
	actorCalls    atomic.Int32
}

func (f *fakePlane) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /actor-tokens", func(w http.ResponseWriter, r *http.Request) {
		f.actorCalls.Add(1)
		if f.actorRejects {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized","message":"audience mismatch"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"actor_token": "actor-tok"})
	})
	mux.HandleFunc("POST /v1/tokens/exchange", func(w http.ResponseWriter, r *http.Request) {
		f.exchangeCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "fresh-cella-tok"})
	})
	mux.HandleFunc("GET /v1/sandboxes", func(w http.ResponseWriter, r *http.Request) {
		f.sandboxCalls.Add(1)
		want, _ := f.acceptBearer.Load().(string)
		if r.Header.Get("Authorization") != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized","message":"token revoked"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	return mux
}

// seedTokens isolates both token files in t.TempDir and seeds token.json
// and, unless rootAccess is empty, auth-token.json.
func seedTokens(t *testing.T, cellaTok Token, rootAccess string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(dir, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))
	if err := SaveToken("", cellaTok); err != nil {
		t.Fatal(err)
	}
	if rootAccess != "" {
		if err := SaveAuthToken(Token{
			AccessToken: rootAccess,
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(time.Hour),
			IssuedAt:    time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientRefreshesOn401AndRetriesOnce(t *testing.T) {
	f := &fakePlane{t: t}
	f.acceptBearer.Store("fresh-cella-tok")
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	seedTokens(t, Token{AccessToken: "stale-tok", TokenType: "Bearer"}, "root-access")

	c := NewClient(srv.URL)
	var out any
	if err := c.GetJSON(context.Background(), "/v1/sandboxes", &out); err != nil {
		t.Fatalf("GetJSON after refresh: %v", err)
	}
	if got := f.sandboxCalls.Load(); got != 2 {
		t.Errorf("sandbox calls = %d, want 2 (401 then retried)", got)
	}
	saved, err := LoadToken("")
	if err != nil || saved.AccessToken != "fresh-cella-tok" {
		t.Errorf("token.json = %q, %v; want persisted fresh-cella-tok", saved.AccessToken, err)
	}
}

func TestClientProactiveRefreshOnKnownExpiry(t *testing.T) {
	f := &fakePlane{t: t}
	f.acceptBearer.Store("fresh-cella-tok")
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	seedTokens(t, Token{
		AccessToken: "stale-tok",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(10 * time.Second), // inside the 60s window
	}, "root-access")

	c := NewClient(srv.URL)
	var out any
	if err := c.GetJSON(context.Background(), "/v1/sandboxes", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got := f.sandboxCalls.Load(); got != 1 {
		t.Errorf("sandbox calls = %d, want 1 (refreshed before sending)", got)
	}
}

func TestClientRefreshSkipsSilentlyWithoutAuthRoot(t *testing.T) {
	f := &fakePlane{t: t}
	f.acceptBearer.Store("something-else")
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	seedTokens(t, Token{AccessToken: "pasted-tok", TokenType: "Bearer"}, "")

	c := NewClient(srv.URL)
	var out any
	err := c.GetJSON(context.Background(), "/v1/sandboxes", &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want the original 401 (paste-mode has no refresh path)", err)
	}
	if got := f.exchangeCalls.Load(); got != 0 {
		t.Errorf("exchange calls = %d, want 0", got)
	}
}

func TestClientDoesNotRetryUnseekableBody(t *testing.T) {
	f := &fakePlane{t: t}
	f.acceptBearer.Store("fresh-cella-tok")
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	seedTokens(t, Token{AccessToken: "stale-tok", TokenType: "Bearer"}, "root-access")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/things", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"nope"}`))
	})
	postSrv := httptest.NewServer(mux)
	defer postSrv.Close()

	c := NewClient(postSrv.URL)
	body := io.LimitReader(strings.NewReader(`{"x":1}`), 7) // not an io.Seeker
	err := c.Do(context.Background(), http.MethodPost, "/v1/things", body, "application/json", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want the original 401 (unseekable body must not retry)", err)
	}
}

func TestRefreshCellaTokenAudienceMismatchFallsBackToRoot(t *testing.T) {
	f := &fakePlane{t: t, actorRejects: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)
	seedTokens(t, Token{AccessToken: "stale-tok", TokenType: "Bearer"}, "root-access")

	tok, ok := RefreshCellaToken(context.Background(), srv.URL)
	if !ok || tok != "fresh-cella-tok" {
		t.Fatalf("RefreshCellaToken = %q, %v; want fresh-cella-tok via direct exchange", tok, ok)
	}
	if f.exchangeCalls.Load() != 1 {
		t.Errorf("exchange calls = %d, want 1", f.exchangeCalls.Load())
	}
}
