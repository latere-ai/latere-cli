// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestPKCEChallengeMatchesVerifier(t *testing.T) {
	p, err := newPKCE()
	if err != nil {
		t.Fatalf("newPKCE: %v", err)
	}
	if p.verifier == "" || p.challenge == "" {
		t.Fatal("empty verifier/challenge")
	}
	sum := sha256.Sum256([]byte(p.verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); p.challenge != want {
		t.Fatalf("challenge = %q, want S256(verifier) = %q", p.challenge, want)
	}
}

func TestBuildClaudeAuthorizeURL(t *testing.T) {
	p := pkce{verifier: "ver", challenge: "chal"}
	u, err := url.Parse(buildClaudeAuthorizeURL(p))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	checks := map[string]string{
		"client_id":             claudeOAuthClientID,
		"response_type":         "code",
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
		"state":                 "ver",
		"redirect_uri":          claudeRedirectURI,
	}
	for k, want := range checks {
		if q.Get(k) != want {
			t.Errorf("authorize URL %s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestExchangeAndStoreClaudeToken(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "sk-ant-oat-new", "refresh_token": "rt-1", "expires_in": 3600,
		})
	}))
	defer srv.Close()
	old := claudeTokenURL
	claudeTokenURL = srv.URL
	defer func() { claudeTokenURL = old }()
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))

	tok, err := exchangeClaudeCode(context.Background(), srv.Client(), "AUTHCODE#STATEXYZ", pkce{verifier: "ver"})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	// The pasted "code#state" is split correctly and the verifier is sent.
	if gotBody["code"] != "AUTHCODE" || gotBody["state"] != "STATEXYZ" || gotBody["code_verifier"] != "ver" {
		t.Fatalf("token request body = %v", gotBody)
	}
	if tok.AccessToken != "sk-ant-oat-new" || tok.RefreshToken != "rt-1" || tok.ExpiresAt.IsZero() {
		t.Fatalf("token = %+v", tok)
	}

	if err := saveClaudeToken(tok); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadClaudeToken()
	if err != nil || got.AccessToken != "sk-ant-oat-new" {
		t.Fatalf("load = (%+v, %v)", got, err)
	}
}

func TestClaudeOAuthBearerRefreshes(t *testing.T) {
	var refreshed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(b, &body)
		if body["grant_type"] == "refresh_token" {
			refreshed = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "sk-ant-oat-refreshed", "refresh_token": "rt-2", "expires_in": 3600,
		})
	}))
	defer srv.Close()
	old := claudeTokenURL
	claudeTokenURL = srv.URL
	defer func() { claudeTokenURL = old }()
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))

	// An expired token with a refresh token → bearer refreshes it.
	_ = saveClaudeToken(claudeToken{AccessToken: "old", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(-time.Hour)})
	tok, err := claudeOAuthBearer(context.Background())
	if err != nil {
		t.Fatalf("claudeOAuthBearer: %v", err)
	}
	if !refreshed || tok != "sk-ant-oat-refreshed" {
		t.Fatalf("bearer = %q, refreshed = %v", tok, refreshed)
	}

	// No stored token → ("", nil) so callers can prompt.
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "absent.json"))
	if tok, err := claudeOAuthBearer(context.Background()); err != nil || tok != "" {
		t.Fatalf("no-login bearer = (%q, %v), want empty", tok, err)
	}
}

func TestBuildLocalModelUsesStoredClaudeLogin(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_AUTO", "")
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))
	// A valid stored login (far-future expiry) → buildLocalModel returns a model.
	_ = saveClaudeToken(claudeToken{AccessToken: "sk-ant-oat-stored", ExpiresAt: time.Now().Add(time.Hour)})
	m, err := buildLocalModel(context.Background(), "")
	if err != nil || m == nil {
		t.Fatalf("buildLocalModel with stored login = (%v, %v)", m, err)
	}
}
