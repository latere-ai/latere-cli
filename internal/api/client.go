// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package api is the CLI's plain HTTP client, which the Topos commands and
// the issuer calls share, with the saved login, the actor-token mint and the
// token refresh they rest on. A client carries a bearer the caller supplies:
// an actor token minted for one product from the login saved by
// `latere login`, or the login itself at the issuer.
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
	"strings"
	"time"

	"latere.ai/x/pkg/otel"
)

// Client wraps the HTTP plumbing. Build with NewClient, then attach the
// bearer with SetBearer.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client

	// Refresh, when set, re-mints the bearer and returns it with its own
	// expiry. It runs before a request whose held token is within
	// remintMargin of a known expiry, and once more on a 401. A product
	// token lives five minutes and a transfer or a log follow outlives
	// several of them, so this is per request, not once per client.
	Refresh func(ctx context.Context) (string, time.Time, bool)

	// expiresAt is when the held bearer lapses. Zero means unknown and
	// skips the proactive re-mint; the 401 path still applies.
	expiresAt time.Time
}

// remintMargin is how long before expiry the bearer is replaced: enough
// for the request it is attached to, and its retry, to reach the product
// before the token lapses.
const remintMargin = 60 * time.Second

// NewClient builds a Client for apiURL, which the caller has resolved from
// its own flag, environment and default. It carries no credential: the
// caller attaches the bearer for the service it is about to call.
func NewClient(apiURL string) *Client {
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
}

// remintIfDue replaces the bearer before sending when the held one is
// within remintMargin of a known expiry. A mint that fails leaves the
// held token in place with an unknown expiry, so the next request does
// not ask again; a refusal from the product still triggers one retry.
func (c *Client) remintIfDue(ctx context.Context) {
	if c.Refresh == nil || c.expiresAt.IsZero() || time.Now().Before(c.expiresAt.Add(-remintMargin)) {
		return
	}
	token, expiry, ok := c.Refresh(ctx)
	if !ok {
		c.expiresAt = time.Time{}
		return
	}
	c.SetBearer(token, expiry)
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

// APIError is a refusal in the flat envelope of the issuer and Topos: a code,
// a message and a request id.
type APIError struct {
	Status  int    `json:"-"` // HTTP response status, never supplied by the JSON envelope.
	Code    string `json:"code"`
	Message string `json:"message"`
	ReqID   string `json:"request_id,omitempty"`
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("status %d: %s", e.Status, e.Message)
}

// Do executes the request and decodes exactly one JSON value into out,
// requiring a complete response. A nil out discards the response body
// but still reports transfer errors. When a Refresh hook is set it runs
// before a request whose held token is within remintMargin of expiry, and
// once more after a 401, retrying the request with the fresh bearer (only
// when the body is nil or rewindable, so a consumed stream is never resent
// corrupt).
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	c.remintIfDue(ctx)
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
		return c.HTTP.Do(req)
	}
	resp, err := send()
	if err != nil {
		return err
	}
	// One retry per request: a lapsed token is replaced and the request
	// sent again, and the second refusal is the caller's to report.
	if resp.StatusCode == http.StatusUnauthorized && c.Refresh != nil {
		rewindable := body == nil || buffered != nil
		if rewindable {
			if token, expiry, ok := c.Refresh(ctx); ok {
				// The rejected body is no longer needed. Draining it can
				// block the retry indefinitely if the server stalls.
				_ = resp.Body.Close()
				c.SetBearer(token, expiry)
				resp, err = send()
				if err != nil {
					return err
				}
			}
		}
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeResponse(resp, out)
}

func decodeResponse(resp *http.Response, out any) error {
	if resp.StatusCode/100 != 2 {
		return parseAPIError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	return decodeJSONResponse(resp.Body, out)
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
