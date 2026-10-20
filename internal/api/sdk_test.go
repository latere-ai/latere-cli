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

// Without a saved login, a product command fails with a *latere.TokenError
// that wraps ErrNoToken, which the commands turn into the hint to sign in,
// and sends nothing to the product, retries included.
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
	if _, ok := errors.AsType[*latere.TokenError](err); !ok {
		t.Errorf("err = %v, want a *latere.TokenError", err)
	}
	if got, _ := api.bearer.Load().(string); got != "" {
		t.Errorf("the API was sent a request, with bearer %q", got)
	}
}

// One command mints one actor token however many requests it sends, and a
// product's passing outage is ridden out: a read the product answered 503
// is sent again and succeeds.
func TestProductConfigMintsOnceAndRidesOutAPassingOutage(t *testing.T) {
	issuer := newIssuerStub(t)
	seedLogin(t, Token{AccessToken: "login-access", ExpiresAt: time.Now().Add(time.Hour)})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg, err := Product{BaseURL: srv.URL, AuthURL: issuer.srv.URL, Audience: "insula"}.Config()
	if err != nil {
		t.Fatal(err)
	}
	get := &latere.Request{Method: http.MethodGet, Path: "/"}
	for range 2 {
		if err := cfg.Do(t.Context(), get, nil); err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	if n := requests.Load(); n != 3 {
		t.Errorf("requests = %d, want 3: two reads, one sent again after its 503", n)
	}
	if n := issuer.mints.Load(); n != 1 {
		t.Errorf("mints = %d, want one", n)
	}
}

// A product that answers 401 is sent the request once more with an actor
// token minted again, since the held one may have been revoked or expired
// early by the product's clock; a second 401 is reported.
func TestProductConfigMintsAgainAfterA401(t *testing.T) {
	issuer := newIssuerStub(t)
	seedLogin(t, Token{AccessToken: "login-access", ExpiresAt: time.Now().Add(time.Hour)})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg, err := Product{BaseURL: srv.URL, AuthURL: issuer.srv.URL, Audience: "insula"}.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Do(t.Context(), &latere.Request{Method: http.MethodGet, Path: "/"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2: the 401 and the request sent again", n)
	}
	if n := issuer.mints.Load(); n != 2 {
		t.Errorf("mints = %d, want 2: one dropped after the 401", n)
	}
}
