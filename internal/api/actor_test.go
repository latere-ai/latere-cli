// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"latere.ai/x/pkg/authkit/oidc"
)

// issuerStub is auth: /actor-tokens mints, /token refreshes. It records
// what each request carried so a test can assert on the wire rather than
// on the value returned.
type issuerStub struct {
	srv *httptest.Server

	mints    atomic.Int32
	refresh  atomic.Int32
	audience atomic.Value // string
	ttl      atomic.Int64
	bearer   atomic.Value // string

	status int    // non-zero: what /actor-tokens answers instead of minting
	reason string // the body that goes with it
}

func newIssuerStub(t *testing.T) *issuerStub {
	t.Helper()
	s := &issuerStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /actor-tokens", func(w http.ResponseWriter, r *http.Request) {
		s.mints.Add(1)
		s.bearer.Store(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		var body struct {
			Audience string `json:"audience"`
			TTL      int64  `json:"ttl_seconds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.audience.Store(body.Audience)
		s.ttl.Store(body.TTL)
		if s.status != 0 {
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.reason))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actor_token": "actor-for-" + body.Audience,
			"expires_in":  body.TTL,
		})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		s.refresh.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"refreshed-login","refresh_token":"next-refresh","token_type":"Bearer","expires_in":3600}`))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *issuerStub) str(v atomic.Value) string {
	got, _ := v.Load().(string)
	return got
}

// seedLogin isolates the token file in t.TempDir and writes one login.
func seedLogin(t *testing.T, tok Token) {
	t.Helper()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	if tok.AccessToken == "" {
		return
	}
	if err := SaveAuthToken(tok); err != nil {
		t.Fatal(err)
	}
}

// The CLI asks the issuer for the longest lifetime it grants, for one
// audience, presenting the login token. Anything else would be a second
// minting shape beside the one the shared library defines.
func TestActorTokenAsksTheIssuerForOneAudience(t *testing.T) {
	s := newIssuerStub(t)
	seedLogin(t, Token{AccessToken: "login-access", ExpiresAt: time.Now().Add(time.Hour)})

	token, expiry, err := ActorToken(t.Context(), s.srv.URL, "sandboxd")
	if err != nil {
		t.Fatalf("ActorToken: %v", err)
	}
	if token != "actor-for-sandboxd" {
		t.Errorf("token = %q, want the issuer's actor token", token)
	}
	if got := s.str(s.audience); got != "sandboxd" {
		t.Errorf("audience = %q, want sandboxd", got)
	}
	if want := int64(oidc.ActorTokenLifetime / time.Second); s.ttl.Load() != want {
		t.Errorf("ttl_seconds = %d, want %d", s.ttl.Load(), want)
	}
	if got := s.str(s.bearer); got != "login-access" {
		t.Errorf("mint presented %q, want the login token", got)
	}
	if d := time.Until(expiry); d < 4*time.Minute || d > oidc.ActorTokenLifetime {
		t.Errorf("expiry in %v, want about %v", d, oidc.ActorTokenLifetime)
	}
}

// A login within a minute of expiry is refreshed before it mints, and the
// refreshed token is what reaches the issuer.
func TestActorTokenRefreshesAnExpiringLogin(t *testing.T) {
	s := newIssuerStub(t)
	seedLogin(t, Token{
		AccessToken:  "stale-login",
		RefreshToken: "login-refresh",
		ExpiresAt:    time.Now().Add(10 * time.Second),
	})

	if _, _, err := ActorToken(t.Context(), s.srv.URL, "toposd"); err != nil {
		t.Fatalf("ActorToken: %v", err)
	}
	if s.refresh.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", s.refresh.Load())
	}
	if got := s.str(s.bearer); got != "refreshed-login" {
		t.Errorf("mint presented %q, want the refreshed login", got)
	}
	saved, err := LoadAuthToken()
	if err != nil || saved.AccessToken != "refreshed-login" {
		t.Errorf("saved login = %q (%v), want the refreshed one", saved.AccessToken, err)
	}
}

func TestActorTokenWithoutALoginIsErrNoToken(t *testing.T) {
	s := newIssuerStub(t)
	seedLogin(t, Token{})

	_, _, err := ActorToken(t.Context(), s.srv.URL, "origo")
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if s.mints.Load() != 0 {
		t.Error("a mint was attempted with no login on file")
	}
}

func TestActorTokenExpiredLoginWithoutRefreshGrant(t *testing.T) {
	s := newIssuerStub(t)
	seedLogin(t, Token{AccessToken: "pasted", ExpiresAt: time.Now().Add(-time.Hour)})

	_, _, err := ActorToken(t.Context(), s.srv.URL, "origo")
	if err == nil || !strings.Contains(err.Error(), "run `latere login`") {
		t.Fatalf("err = %v, want a re-login instruction", err)
	}
	if s.mints.Load() != 0 {
		t.Error("a mint was attempted with an unusable login")
	}
}

// A refusal carries the issuer's status and reason: the client is
// registered for five audiences and asking for a sixth is a 403, which a
// user can only act on if the reason survives.
func TestActorTokenCarriesTheIssuersRefusal(t *testing.T) {
	s := newIssuerStub(t)
	s.status, s.reason = http.StatusForbidden, "audience not registered for this client"
	seedLogin(t, Token{AccessToken: "login-access", ExpiresAt: time.Now().Add(time.Hour)})

	_, _, err := ActorToken(t.Context(), s.srv.URL, "not-a-product")
	if err == nil {
		t.Fatal("a refused audience minted a token")
	}
	for _, want := range []string{"403", "audience not registered", "latere login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
}

func TestResolveAuthURLPrefersExplicitThenEnvironment(t *testing.T) {
	t.Setenv("AUTH_URL", "https://auth.env.example/")
	if got := ResolveAuthURL("https://cella.other.example", "https://auth.flag.example/"); got != "https://auth.flag.example" {
		t.Errorf("explicit value = %q", got)
	}
	if got := ResolveAuthURL("https://cella.other.example", ""); got != "https://auth.env.example" {
		t.Errorf("environment value = %q", got)
	}
	t.Setenv("AUTH_URL", "")
	if got := ResolveAuthURL("https://cella.other.example", ""); got != "https://auth.other.example" {
		t.Errorf("inferred value = %q", got)
	}
	if got := ResolveAuthURL("", ""); got != DefaultAuthURL {
		t.Errorf("default value = %q", got)
	}
}
