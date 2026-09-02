// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

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

func TestPostAndStoreClaudeToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "sk-ant-oat-new", "refresh_token": "rt-1", "expires_in": 3600,
		})
	}))
	defer srv.Close()
	old := claudeTokenURL
	claudeTokenURL = srv.URL
	defer func() { claudeTokenURL = old }()
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))

	tok, err := postClaudeToken(context.Background(), srv.Client(), []byte(`{"grant_type":"authorization_code"}`))
	if err != nil {
		t.Fatalf("postClaudeToken: %v", err)
	}
	if tok.AccessToken != "sk-ant-oat-new" || tok.RefreshToken != "rt-1" || tok.ExpiresAt.IsZero() {
		t.Fatalf("token = %+v", tok)
	}
	if err := saveClaudeToken(tok); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got, err := loadClaudeToken(); err != nil || got.AccessToken != "sk-ant-oat-new" {
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
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	// No latere/Lux token, so the legacy Claude-login fallback is exercised.
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))
	// A valid stored login (far-future expiry) → buildLocalModel returns a model.
	_ = saveClaudeToken(claudeToken{AccessToken: "sk-ant-oat-stored", ExpiresAt: time.Now().Add(time.Hour)})
	m, err := buildLocalModel(context.Background(), "")
	if err != nil || m == nil {
		t.Fatalf("buildLocalModel with stored login = (%v, %v)", m, err)
	}
}
