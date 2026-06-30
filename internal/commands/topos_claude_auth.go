// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude (Anthropic) OAuth login for `latere topos --local`, the same PKCE flow
// other local harnesses (opencode, Claude Code) use. It mints an sk-ant-oat
// access token that the SDK's Anthropic adapter sends as a Bearer with the OAuth
// beta. Owning our own token means --local does not piggyback on (and get
// rate-limited alongside) a shared CLAUDE_CODE_OAUTH_TOKEN.
const (
	// claudeOAuthClientID is the public OAuth client id for the Claude desktop /
	// CLI integration (no secret; PKCE protects the exchange).
	claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeAuthorizeURL  = "https://claude.ai/oauth/authorize"
	claudeRedirectURI   = "https://console.anthropic.com/oauth/code/callback"
	claudeScopes        = "org:create_api_key user:profile user:inference"
)

// claudeTokenURL is the OAuth token endpoint; a var so tests can point it at a
// fake server.
var claudeTokenURL = "https://console.anthropic.com/v1/oauth/token"

// claudeToken is the stored Claude OAuth credential.
type claudeToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// claudeTokenPath is where the Claude OAuth token is stored
// (~/.config/latere/claude.json), overridable for tests.
func claudeTokenPath() string {
	if p := os.Getenv("LATERE_CLAUDE_TOKEN_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "latere", "claude.json")
}

func loadClaudeToken() (claudeToken, error) {
	var t claudeToken
	b, err := os.ReadFile(claudeTokenPath())
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

func saveClaudeToken(t claudeToken) error {
	p := claudeTokenPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// pkce holds a PKCE verifier and its S256 challenge.
type pkce struct {
	verifier  string
	challenge string
}

func newPKCE() (pkce, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return pkce{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return pkce{verifier: verifier, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// claudeAuthorizeURL builds the authorize URL for the manual-paste flow
// (code=true makes the callback page display the code for the user to paste).
func buildClaudeAuthorizeURL(p pkce) string {
	q := url.Values{
		"code":                  {"true"},
		"client_id":             {claudeOAuthClientID},
		"response_type":         {"code"},
		"redirect_uri":          {claudeRedirectURI},
		"scope":                 {claudeScopes},
		"code_challenge":        {p.challenge},
		"code_challenge_method": {"S256"},
		"state":                 {p.verifier},
	}
	return claudeAuthorizeURL + "?" + q.Encode()
}

// exchangeClaudeCode trades the pasted "code#state" for tokens. The callback
// page returns the authorization code and state joined by '#'.
func exchangeClaudeCode(ctx context.Context, httpc *http.Client, pasted string, p pkce) (claudeToken, error) {
	code, state, _ := strings.Cut(strings.TrimSpace(pasted), "#")
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"state":         state,
		"client_id":     claudeOAuthClientID,
		"redirect_uri":  claudeRedirectURI,
		"code_verifier": p.verifier,
	})
	return postClaudeToken(ctx, httpc, body)
}

// refreshClaudeToken renews an expired access token using the refresh token.
func refreshClaudeToken(ctx context.Context, httpc *http.Client, refresh string) (claudeToken, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
		"client_id":     claudeOAuthClientID,
	})
	return postClaudeToken(ctx, httpc, body)
}

func postClaudeToken(ctx context.Context, httpc *http.Client, body []byte) (claudeToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenURL, strings.NewReader(string(body)))
	if err != nil {
		return claudeToken{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return claudeToken{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return claudeToken{}, fmt.Errorf("claude oauth: token endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return claudeToken{}, fmt.Errorf("claude oauth: bad token response")
	}
	t := claudeToken{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC(),
	}
	return t, nil
}

// runClaudeLogin drives the interactive login: open the browser, read the pasted
// code, exchange it, and store the token.
func runClaudeLogin(ctx context.Context) error {
	p, err := newPKCE()
	if err != nil {
		return err
	}
	authURL := buildClaudeAuthorizeURL(p)
	fmt.Fprintln(os.Stderr, "Sign in to Claude to authorize the local agent:")
	fmt.Fprintln(os.Stderr, "\n  "+authURL+"\n")
	_ = openBrowser(authURL)
	fmt.Fprint(os.Stderr, "Paste the code shown after you approve, then press Enter:\n> ")

	var pasted string
	if _, err := fmt.Fscanln(os.Stdin, &pasted); err != nil {
		return fmt.Errorf("read code: %w", err)
	}
	tok, err := exchangeClaudeCode(ctx, &http.Client{Timeout: 30 * time.Second}, pasted, p)
	if err != nil {
		return err
	}
	if err := saveClaudeToken(tok); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Signed in to Claude. `latere topos --local` will use this token.")
	return nil
}

// claudeOAuthBearer returns a usable Claude OAuth access token from the stored
// login, refreshing it when expired. Returns ("", nil) when there is no stored
// login (so callers can fall back or prompt).
func claudeOAuthBearer(ctx context.Context) (string, error) {
	t, err := loadClaudeToken()
	if err != nil {
		return "", nil // not logged in
	}
	if t.AccessToken == "" {
		return "", nil
	}
	if t.RefreshToken != "" && !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt.Add(-60*time.Second)) {
		refreshed, rerr := refreshClaudeToken(ctx, &http.Client{Timeout: 30 * time.Second}, t.RefreshToken)
		if rerr != nil {
			return "", fmt.Errorf("claude token expired and refresh failed (%v); run `latere topos login`", rerr)
		}
		if refreshed.RefreshToken == "" {
			refreshed.RefreshToken = t.RefreshToken
		}
		_ = saveClaudeToken(refreshed)
		return refreshed.AccessToken, nil
	}
	return t.AccessToken, nil
}
