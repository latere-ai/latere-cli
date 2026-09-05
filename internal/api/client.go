// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package api is the HTTP client every `latere sandbox …` command
// shares. Uses the public sandboxd surface at cella.latere.ai. The
// client carries a Bearer token loaded from ~/.config/latere/token.json,
// written by `latere login`.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"latere.ai/x/pkg/atomicfile"
	"latere.ai/x/pkg/otel"

	"github.com/latere-ai/latere-cli/internal/config"
)

// DefaultAPIURL is overridden by SANDBOX_API_URL or --api-url.
const DefaultAPIURL = "https://cella.latere.ai"

// Token is what `latere login` writes to disk. Shape matches an
// OAuth2 token response so an eventual device-code flow can dump its
// reply directly. Only AccessToken is required for now.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	IssuedAt     time.Time `json:"issued_at"`
}

// Client wraps the HTTP plumbing. Build with NewClient.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client

	// Refresh, when set, re-derives the bearer and is invoked at most
	// once per client: proactively when the saved token is within 60s
	// of a known expiry, or reactively after a 401. NewClient wires the
	// default (re-exchange through the retained auth root token); set
	// nil to disable, e.g. when the client must never mint credentials.
	Refresh func(ctx context.Context) (string, bool)

	// expiresAt is token.json's recorded expiry at construction. Zero
	// means unknown (cella's exchange response carries no expiry) and
	// skips the proactive refresh; the 401 path still applies.
	expiresAt time.Time
	refreshed bool
}

// NewClient builds a Client from env + the token file. Returns the
// client even when the token file is missing — commands that need auth
// will fail with a clear error, but `--help` and `auth login` work.
func NewClient(apiURL string) *Client {
	if apiURL == "" {
		if v := os.Getenv("SANDBOX_API_URL"); v != "" {
			apiURL = v
		} else {
			apiURL = DefaultAPIURL
		}
	}
	apiURL = strings.TrimRight(apiURL, "/")
	tok, _ := LoadToken("")
	c := &Client{
		BaseURL:   apiURL,
		Token:     tok.AccessToken,
		HTTP:      &http.Client{Timeout: 60 * time.Second, Transport: otel.Transport(nil)},
		expiresAt: tok.ExpiresAt,
	}
	c.Refresh = func(ctx context.Context) (string, bool) {
		return RefreshCellaToken(ctx, c.BaseURL)
	}
	return c
}

// RefreshCellaToken re-derives the cella bearer from the retained auth
// root token: refresh the root when it is expiring, mint a sandboxd
// actor token, exchange it at /v1/tokens/exchange, and persist the
// replacement (cella's catalog replaces the previous row by label, so
// this rotates the credential). Returns ok=false when no auth root
// token exists (paste-mode login has none) or any step fails; callers
// keep their original error.
func RefreshCellaToken(ctx context.Context, apiBase string) (string, bool) {
	authTok, err := LoadAuthToken()
	if err != nil || authTok.AccessToken == "" {
		return "", false
	}
	authBase := strings.TrimRight(os.Getenv("AUTH_URL"), "/")
	if authBase == "" {
		authBase = InferAuthURL(apiBase)
	}
	access := authTok.AccessToken
	if authTok.RefreshToken != "" && !authTok.ExpiresAt.IsZero() &&
		time.Now().After(authTok.ExpiresAt.Add(-60*time.Second)) {
		refreshed, rerr := RefreshAuthToken(ctx, authBase, authTok)
		if rerr != nil {
			return "", false
		}
		access = refreshed.AccessToken
	}
	httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil)}
	bearer, err := MintActorToken(ctx, httpc, authBase, access, "sandboxd", 60)
	if errors.Is(err, ErrActorAudienceMismatch) {
		// Legacy audience shape: the root token itself is accepted by
		// sandboxd, mirror the login-time fallback.
		bearer = access
	} else if err != nil {
		return "", false
	}
	cellaTok, err := ExchangeAtCella(ctx, httpc, apiBase, bearer)
	if err != nil {
		return "", false
	}
	_ = SaveToken("", Token{
		AccessToken: cellaTok,
		TokenType:   "Bearer",
		IssuedAt:    time.Now().UTC(),
	}) // best-effort; the in-memory token still works this run
	return cellaTok, true
}

// ---- token storage ----

// TokenPath returns the canonical path to token.json (the Cella-issued
// bearer used by `latere cella`/`whoami`). Callers can override with
// LATERE_TOKEN_FILE for testing.
func TokenPath() string {
	return tokenFilePath("LATERE_TOKEN_FILE", "token.json")
}

// AuthTokenPath returns the path to auth-token.json — the retained
// auth.latere.ai root token (access + refresh) used to mint short-lived
// Lux actor tokens. Kept separate from token.json because the two have
// different issuers and lifecycles; clobbering one must never disturb
// the other. Override with LATERE_AUTH_TOKEN_FILE for testing.
func AuthTokenPath() string {
	return tokenFilePath("LATERE_AUTH_TOKEN_FILE", "auth-token.json")
}

// tokenFilePath resolves a token file: the env override wins, otherwise
// $XDG_CONFIG_HOME/latere/<name> (falling back to ~/.config).
func tokenFilePath(envVar, name string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return config.Path(name)
}

// ErrNoToken means the file does not exist (the user hasn't logged in).
var ErrNoToken = errors.New("not logged in; run `latere login`")

// LoadToken reads token.json. Empty path uses TokenPath().
func LoadToken(path string) (Token, error) {
	if path == "" {
		path = TokenPath()
	}
	if path == "" {
		return Token{}, ErrNoToken
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Token{}, ErrNoToken
		}
		return Token{}, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return Token{}, fmt.Errorf("parse token file: %w", err)
	}
	return t, nil
}

// SaveToken atomically replaces token.json with 0600 perms, syncing the write
// before publishing it. Creates the directory if missing. Empty path uses
// TokenPath(). Permissions are best-effort on Windows.
func SaveToken(path string, t Token) error {
	if path == "" {
		path = TokenPath()
	}
	if path == "" {
		return errors.New("cannot determine token path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteSync(path, b, 0o600)
}

// LoadAuthToken reads the retained auth.latere.ai root token. Returns
// ErrNoToken when the file is absent (login never ran, or ran via the
// --token paste path which produces no auth root token).
func LoadAuthToken() (Token, error) {
	p := AuthTokenPath()
	if p == "" {
		return Token{}, ErrNoToken
	}
	return LoadToken(p)
}

// SaveAuthToken persists the auth.latere.ai root token (0600).
func SaveAuthToken(t Token) error {
	p := AuthTokenPath()
	if p == "" {
		return errors.New("cannot determine auth token path")
	}
	return SaveToken(p, t)
}

// ClearAuthToken deletes auth-token.json. Idempotent.
func ClearAuthToken() error {
	p := AuthTokenPath()
	if p == "" {
		return nil
	}
	return ClearToken(p)
}

// ClearToken deletes token.json. Idempotent.
func ClearToken(path string) error {
	if path == "" {
		path = TokenPath()
	}
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ---- HTTP plumbing ----

// APIError is a structured error from sandboxd's writeErr envelope.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	ReqID   string `json:"request_id,omitempty"`
}

func (e *APIError) Error() string {
	if e.Code == "policy_sidecar_required" {
		return "cannot create cella: the selected policy requires Cella's credential sidecar, but the server has no complete sidecar configuration for this CLI token.\n" +
			"This is not a local command syntax problem. Re-run `latere login` with the latest CLI, then retry.\n" +
			"To choose another policy, run `latere cella policy list` and set `spec.policy` in your Manifest to a selectable policy where sidecar is `no`.\n" +
			"If no such policy is available, ask your Latere admin/support to configure the CLI sidecar client or assign a non-sidecar policy.\n" +
			"server code: policy_sidecar_required"
	}
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("status %d: %s", e.Status, e.Message)
}

// Do executes the request and decodes a JSON response into out. out
// may be nil for endpoints that return no body (or the caller wants
// raw).
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	return c.DoWithHeaders(ctx, method, path, body, contentType, nil, out)
}

// DoWithHeaders is Do with extra request headers (e.g. Idempotency-Key).
// When a Refresh hook is set it fires at most once: before the request
// if the saved token has a known expiry within 60s, or after a 401,
// retrying the request once with the fresh bearer (only when the body
// is nil or rewindable, so a consumed stream is never resent corrupt).
func (c *Client) DoWithHeaders(ctx context.Context, method, path string, body io.Reader, contentType string, headers map[string]string, out any) error {
	if c.Refresh != nil && !c.refreshed && !c.expiresAt.IsZero() &&
		time.Now().After(c.expiresAt.Add(-60*time.Second)) {
		c.refreshed = true
		if t, ok := c.Refresh(ctx); ok {
			c.Token = t
			c.expiresAt = time.Time{}
		}
	}
	// A rewindable body is read once and each attempt gets its own reader.
	// Seeking the caller's reader back after a 401 raced the transport,
	// which may still be writing the first attempt when the response
	// arrives, and the retry then sent whatever was left: nothing.
	var buffered []byte
	if _, ok := body.(io.Seeker); ok {
		b, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		buffered = b
	}
	send := func() (*http.Response, error) {
		attempt := body
		if buffered != nil {
			attempt = bytes.NewReader(buffered)
		}
		req, err := c.req(ctx, method, path, attempt, contentType)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return c.HTTP.Do(req)
	}
	resp, err := send()
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.Refresh != nil && !c.refreshed {
		rewindable := body == nil || buffered != nil
		if rewindable {
			c.refreshed = true
			if t, ok := c.Refresh(ctx); ok {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				c.Token = t
				resp, err = send()
				if err != nil {
					return err
				}
			}
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return parseAPIError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// DoRaw runs the request and returns the response so the caller can
// stream the body (used for files/export and SSE log follow). Streaming
// requests do not auto-refresh the bearer; a 401 surfaces to the caller.
func (c *Client) DoRaw(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := c.req(ctx, method, path, body, contentType)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		return nil, parseAPIError(resp)
	}
	return resp, nil
}

// PostJSON is a convenience over Do for the common POST-JSON-decode-JSON
// pattern.
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, path, bytes.NewReader(b), "application/json", out)
}

// GetJSON is the GET variant.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, "", out)
}

func (c *Client) req(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "latere-cli")
	return req, nil
}

func parseAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	e := &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	_ = json.Unmarshal(b, e)
	return e
}

// PathEscape is a re-export of url.PathEscape so callers don't need to
// import net/url alongside this package.
func PathEscape(s string) string { return url.PathEscape(s) }

// MustRequireAuth returns ErrNoToken when the client has no token. Use
// at the start of any command that hits sandboxd.
func (c *Client) MustRequireAuth() error {
	if c.Token == "" {
		return ErrNoToken
	}
	return nil
}
