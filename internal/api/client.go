// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package api is the HTTP client every `latere cella …` command shares.
// It talks to the public Cella surface at cella.latere.ai and carries a
// bearer the caller supplies: an actor token minted for that product from
// the login saved by `latere login`.
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
	"strings"
	"time"

	"latere.ai/x/pkg/otel"
)

// DefaultAPIURL is overridden by SANDBOX_API_URL or --api-url.
const DefaultAPIURL = "https://cella.latere.ai"

// Client wraps the HTTP plumbing. Build with NewClient, then attach the
// bearer with SetBearer.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client

	// Refresh, when set, re-mints the bearer and is invoked at most once
	// per client: proactively when the held token is within a minute of
	// its known expiry, or reactively after a 401. An actor token lives
	// five minutes and a file transfer or a log follow can outlive it,
	// which is what this covers.
	Refresh func(ctx context.Context) (string, bool)

	// expiresAt is when the held bearer lapses. Zero means unknown and
	// skips the proactive re-mint; the 401 path still applies.
	expiresAt time.Time
	refreshed bool
}

// NewClient builds a Client for apiURL, or for $SANDBOX_API_URL, or for
// the public deployment. It carries no credential: `--help` and `latere
// login` need a client before there is anything to present.
func NewClient(apiURL string) *Client {
	if apiURL == "" {
		if v := os.Getenv("SANDBOX_API_URL"); v != "" {
			apiURL = v
		} else {
			apiURL = DefaultAPIURL
		}
	}
	return &Client{
		BaseURL: strings.TrimRight(apiURL, "/"),
		HTTP: &http.Client{
			Timeout: 60 * time.Second, Transport: otel.Transport(nil),
			CheckRedirect: PreserveMethodOnRedirect,
		},
	}
}

// SetBearer attaches the bearer and when it lapses. A zero expiry means
// unknown, which leaves the proactive re-mint off.
func (c *Client) SetBearer(token string, expiry time.Time) {
	c.Token = token
	c.expiresAt = expiry
	c.refreshed = false
}

// PreserveMethodOnRedirect is an http.Client.CheckRedirect callback that
// retains the default ten-request limit and rejects redirects that change
// the HTTP method, which could make a write appear successful after a GET.
func PreserveMethodOnRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if previous := via[len(via)-1].Method; req.Method != previous {
		return fmt.Errorf("redirect changed request method from %s to %s", previous, req.Method)
	}
	return nil
}

// ---- HTTP plumbing ----

// APIError is a structured error from sandboxd's writeErr envelope.
type APIError struct {
	Status  int    `json:"-"` // HTTP response status, never supplied by the JSON envelope.
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

// Do executes the request and decodes exactly one JSON value into out,
// requiring a complete response. A nil out discards the response body
// but still reports transfer errors. Use DoRaw for streaming responses.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	return c.DoWithHeaders(ctx, method, path, body, contentType, nil, out)
}

// DoWithHeaders is Do with extra request headers (e.g. Idempotency-Key).
// When a Refresh hook is set it fires at most once: before the request
// if the saved token has a known expiry within 60s, or after a 401,
// retrying the request once with the fresh bearer (only when the body
// is nil or rewindable, so a consumed stream is never resent corrupt).
func (c *Client) DoWithHeaders(ctx context.Context, method, path string, body io.Reader, contentType string, headers map[string]string, out any) error {
	return c.doWithHeaders(ctx, method, path, body, contentType, headers, func(resp *http.Response) error {
		return decodeResponse(resp, out, 0)
	})
}

// doWithHeaders owns response closure and shares authentication/retry behavior
// between ordinary requests and endpoints with structured error results.
func (c *Client) doWithHeaders(ctx context.Context, method, path string, body io.Reader, contentType string, headers map[string]string, decode func(*http.Response) error) error {
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
				// The rejected body is no longer needed. Draining it can
				// block the retry indefinitely if the server stalls.
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
	return decode(resp)
}

func decodeResponse(resp *http.Response, out any, extraStatus int) error {
	if resp.StatusCode/100 != 2 && (extraStatus == 0 || resp.StatusCode != extraStatus) {
		return parseAPIError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	return decodeJSONResponse(resp.Body, out)
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

// PostJSONWithStatus also decodes extraStatus as a complete JSON response.
// It returns the actual HTTP status. Callers must validate the extra-status
// payload and report its failure; ordinary non-2xx statuses remain APIError.
func (c *Client) PostJSONWithStatus(ctx context.Context, path string, body, out any, extraStatus int) (int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	var status int
	err = c.doWithHeaders(ctx, http.MethodPost, path, bytes.NewReader(b), "application/json", nil, func(resp *http.Response) error {
		status = resp.StatusCode
		return decodeResponse(resp, out, extraStatus)
	})
	return status, err
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

// decodeJSONResponse requires one JSON value followed by a clean end of body.
func decodeJSONResponse(body io.Reader, out any) error {
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	// Decode can finish before the transport reports a truncated body.
	// Require EOF after the value and any legal trailing whitespace.
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("response from API contains multiple JSON values")
	}
	return nil
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
