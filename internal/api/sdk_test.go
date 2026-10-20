// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"latere.ai/x/sdk-go/latere"
)

// apiStub records the bearer and User-Agent of every request it answers.
type apiStub struct {
	srv       *httptest.Server
	bearer    atomic.Value // string
	userAgent atomic.Value // string
}

func newAPIStub(t *testing.T) *apiStub {
	t.Helper()
	s := &apiStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.bearer.Store(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		s.userAgent.Store(r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// A product command presents an actor token minted from the saved login for
// its own audience, never the login token, and names the CLI in its
// User-Agent.
func TestProductConfigPresentsAnActorTokenForItsAudience(t *testing.T) {
	issuer := newIssuerStub(t)
	seedLogin(t, Token{AccessToken: "login-access", ExpiresAt: time.Now().Add(time.Hour)})
	api := newAPIStub(t)

	cfg, err := Product{BaseURL: api.srv.URL, AuthURL: issuer.srv.URL, Audience: "insula"}.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Do(t.Context(), &latere.Request{Method: http.MethodGet, Path: "/v1/apps/apps"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got := issuer.str(issuer.bearer); got != "login-access" {
		t.Errorf("the mint presented %q, want the saved login", got)
	}
	if got := issuer.str(issuer.audience); got != "insula" {
		t.Errorf("the mint asked for %q, want insula", got)
	}
	if got, _ := api.bearer.Load().(string); got != "actor-for-insula" {
		t.Errorf("the API saw bearer %q, want the actor token", got)
	}
	if got, _ := api.userAgent.Load().(string); !strings.HasPrefix(got, "latere-cli ") {
		t.Errorf("User-Agent %q does not start with latere-cli", got)
	}
}

// A token the person exported is presented as given, and nothing is minted.
func TestProductConfigPresentsAGivenTokenAsIs(t *testing.T) {
	issuer := newIssuerStub(t)
	seedLogin(t, Token{})
	api := newAPIStub(t)

	cfg, err := Product{BaseURL: api.srv.URL, AuthURL: issuer.srv.URL, Audience: "insula", Token: "exported"}.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Do(t.Context(), &latere.Request{Method: http.MethodGet, Path: "/"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got, _ := api.bearer.Load().(string); got != "exported" {
		t.Errorf("the API saw bearer %q, want the exported token", got)
	}
	if n := issuer.mints.Load(); n != 0 {
		t.Errorf("minted %d actor tokens for a given token", n)
	}
}

// Without a saved login, a product command fails with ErrNoToken, which
// the commands turn into the hint to sign in.
func TestProductConfigWithoutALoginIsErrNoToken(t *testing.T) {
	issuer := newIssuerStub(t)
	seedLogin(t, Token{})
	api := newAPIStub(t)

	cfg, err := Product{BaseURL: api.srv.URL, AuthURL: issuer.srv.URL, Audience: "insula"}.Config()
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Do(t.Context(), &latere.Request{Method: http.MethodGet, Path: "/"}, nil)
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}
