// Package api is the HTTP client every `latere sandbox …` command
// shares. Uses the public sandboxd surface at cella.latere.ai. The
// client carries a Bearer token loaded from ~/.config/latere/token.json
// — same file the MCP server (cmd/sandbox-mcp in latere-ai/sandbox)
// reads, so a single `latere auth login` unlocks both consumers.
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
	"runtime"
	"strings"
	"time"
)

// DefaultAPIURL is overridden by SANDBOX_API_URL or --api-url.
const DefaultAPIURL = "https://cella.latere.ai"

// Token is what `latere auth login` writes to disk. Shape matches an
// OAuth2 token response so an eventual device-code flow can dump its
// reply directly. Only AccessToken is required for now.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	IssuedAt     time.Time `json:"issued_at,omitempty"`
}

// Client wraps the HTTP plumbing. Build with NewClient.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
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
	return &Client{
		BaseURL: apiURL,
		Token:   tok.AccessToken,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// ---- token storage ----

// TokenPath returns the canonical path to token.json. Callers can
// override with LATERE_TOKEN_FILE for testing.
func TokenPath() string {
	if v := os.Getenv("LATERE_TOKEN_FILE"); v != "" {
		return v
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "latere", "token.json")
}

// ErrNoToken means the file does not exist (the user hasn't logged in).
var ErrNoToken = errors.New("not logged in; run `latere auth login`")

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

// SaveToken writes token.json with 0600 perms. Creates the directory
// if missing. Empty path uses TokenPath().
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
	// Permission bits aren't enforced on Windows; the WriteFile mode
	// is best-effort cross-platform protection.
	_ = runtime.GOOS // silence unused on platforms without chmod semantics
	return os.WriteFile(path, b, 0o600)
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
			"This is not a local command syntax problem. Re-run `latere auth login` with the latest CLI, then retry.\n" +
			"To choose another policy, run `latere cella policy list` and retry with `latere cella create --policy <name>` using a selectable policy where sidecar is `no`.\n" +
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
	req, err := c.req(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
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
// stream the body (used for files/export and SSE log follow).
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
