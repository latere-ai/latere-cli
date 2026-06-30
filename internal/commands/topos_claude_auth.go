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
	"net"
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

// runClaudeLogin drives the Claude OAuth login (loopback: no copy/paste).
func runClaudeLogin(ctx context.Context) error {
	return loopbackClaudeLogin(ctx)
}

// loopbackClaudeLogin runs the Claude OAuth flow with a localhost redirect, so
// the browser hands the code back automatically — no copy/paste. It stores the
// token and records the provider choice (anthropic / oauth).
func loopbackClaudeLogin(ctx context.Context) error {
	p, err := newPKCE()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("claude login: open loopback: %w", err)
	}
	defer func() { _ = ln.Close() }()
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	q := url.Values{
		"client_id":             {claudeOAuthClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {claudeScopes},
		"code_challenge":        {p.challenge},
		"code_challenge_method": {"S256"},
		"state":                 {p.verifier},
	}
	authURL := claudeAuthorizeURL + "?" + q.Encode()

	type result struct {
		code, state string
	}
	resCh := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qq := r.URL.Query()
		if qq.Get("code") == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "Signed in. You can close this tab and return to the terminal.")
		resCh <- result{code: qq.Get("code"), state: qq.Get("state")}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	fmt.Fprintln(os.Stderr, "Opening your browser to sign in to Claude...")
	fmt.Fprintln(os.Stderr, "If it doesn't open, visit:\n\n  "+authURL+"\n")
	_ = openBrowser(authURL)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resCh:
		body, _ := json.Marshal(map[string]string{
			"grant_type":    "authorization_code",
			"code":          res.code,
			"state":         res.state,
			"client_id":     claudeOAuthClientID,
			"redirect_uri":  redirectURI,
			"code_verifier": p.verifier,
		})
		tok, err := postClaudeToken(ctx, &http.Client{Timeout: 30 * time.Second}, body)
		if err != nil {
			return err
		}
		if err := saveClaudeToken(tok); err != nil {
			return err
		}
		_ = saveProviderConfig(providerConfig{Provider: "anthropic", Method: "oauth"})
		fmt.Fprintln(os.Stderr, "Signed in to Claude.")
		return nil
	}
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
