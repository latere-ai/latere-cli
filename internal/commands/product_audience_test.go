// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/drive"
)

// audienceIssuer is the auth issuer the stub stamps on every token it mints.
// A product credential must never carry it: a token valid at the identity
// service is the account, and an audience-bound one is a single product.
const audienceIssuer = "https://auth.latere.ai"

// productStub is auth and every product on one server. POST /actor-tokens
// mints a token whose payload states the requested audience; every other
// path records the bearer it was presented and answers with an empty body
// each product's decoder accepts.
type productStub struct {
	srv *httptest.Server

	mu        sync.Mutex
	presented []string
}

func newProductStub(t *testing.T) *productStub {
	t.Helper()
	s := &productStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/actor-tokens" {
			var body struct {
				Audience string `json:"audience"`
				TTL      int64  `json:"ttl_seconds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"actor_token": fakeJWT(t, map[string]any{
					"sub": "u-1", "org_id": "o-1", "actor": true,
					"iss": audienceIssuer, "aud": body.Audience,
				}),
				"expires_in": body.TTL,
			})
			return
		}
		s.record(r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/sandboxes" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[],"entries":[],"agents":[]}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// record notes one bearer a product received. The export commands hand
// their token to a caller rather than to a server, so their cases record
// the exported value through the same list.
func (s *productStub) record(bearer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presented = append(s.presented, strings.TrimPrefix(bearer, "Bearer "))
}

func (s *productStub) tokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.presented...)
}

// audienceOf decodes the aud claim of an unsigned test JWT.
func audienceOf(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("bearer %q is not a JWT: the product received a raw credential", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("parse JWT payload: %v", err)
	}
	return claims.Aud
}

// Every CLI path that reaches a product presents a token minted for that
// product and no other. The login token names the auth issuer, so a product
// that received it would hold a credential to the account itself; this is
// the invariant leaf id-01 of specs/infrastructure/identity closes.
func TestProductCredentialsCarryOnlyTheirOwnAudience(t *testing.T) {
	for _, p := range []struct {
		name, audience string
		run            func(t *testing.T, s *productStub)
	}{
		{"cella ls", cellaAudience, func(t *testing.T, s *productStub) {
			t.Setenv("LATERE_CELLA_TOKEN", "")
			t.Setenv("AUTH_URL", s.srv.URL)
			c, err := authedClient(t.Context(), s.srv.URL)
			if err != nil {
				t.Fatalf("cella client: %v", err)
			}
			var out []json.RawMessage
			if err := c.GetJSON(t.Context(), "/v1/sandboxes", &out); err != nil {
				t.Fatalf("cella ls: %v", err)
			}
		}},
		{"drive ls", drive.Audience, func(t *testing.T, s *productStub) {
			t.Setenv("LATERE_DRIVE_TOKEN", "")
			cmd := newDriveCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--drive-url", s.srv.URL, "--auth-url", s.srv.URL, "ls"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("drive ls: %v", err)
			}
		}},
		{"lux models", luxAudience, func(t *testing.T, s *productStub) {
			cmd := newLuxCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"models", "--lux-url", s.srv.URL, "--auth-url", s.srv.URL})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("lux models: %v", err)
			}
		}},
		{"lux env", luxAudience, func(t *testing.T, s *productStub) {
			bearer, _, err := luxEnvBearer(t.Context(), "", s.srv.URL, s.srv.URL)
			if err != nil {
				t.Fatalf("lux env: %v", err)
			}
			s.record(bearer)
		}},
		{"lux session", luxAudience, func(t *testing.T, s *productStub) {
			bearer, err := luxSessionBearer("", s.srv.URL, s.srv.URL)(t.Context())
			if err != nil {
				t.Fatalf("lux serve: %v", err)
			}
			s.record(bearer)
		}},
		{"topos", toposAudience, func(t *testing.T, s *productStub) {
			t.Setenv("TOPOS_TOKEN", "")
			t.Setenv("AUTH_URL", s.srv.URL)
			c, err := toposClient(t.Context(), s.srv.URL)
			if err != nil {
				t.Fatalf("toposClient: %v", err)
			}
			var out struct {
				Agents []json.RawMessage `json:"agents"`
			}
			if err := c.GetJSON(t.Context(), "/v1/agents", &out); err != nil {
				t.Fatalf("topos agents: %v", err)
			}
		}},
		{"git credential", codeAudience, func(t *testing.T, s *productStub) {
			out, err := runGitCredential(t, codeGetInput, "get", "--auth-url", s.srv.URL)
			if err != nil {
				t.Fatalf("git-credential get: %v", err)
			}
			_, password, ok := strings.Cut(strings.TrimSpace(out), "\npassword=")
			if !ok {
				t.Fatalf("git-credential get printed no credential: %q", out)
			}
			s.record(password)
		}},
	} {
		t.Run(p.name, func(t *testing.T) {
			isolateTokens(t)
			isolateBearer(t)
			t.Setenv("AUTH_URL", "")
			writeAuthTokenFile(t, "root-access", "root-refresh", time.Now().Add(time.Hour))
			s := newProductStub(t)
			p.run(t, s)

			got := s.tokens()
			if len(got) == 0 {
				t.Fatal("no credential reached the product")
			}
			for _, bearer := range got {
				if bearer == "root-access" {
					t.Fatalf("%s received the auth root token", p.name)
				}
				if aud := audienceOf(t, bearer); aud != p.audience {
					t.Errorf("%s received aud %q, want %q", p.name, aud, p.audience)
				} else if aud == audienceIssuer {
					t.Errorf("%s received a token for the auth issuer", p.name)
				}
			}
		})
	}
}
