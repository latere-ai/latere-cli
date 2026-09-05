// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
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

	"latere.ai/x/pkg/oidc"
)

// LoginScopes is the single scope set the CLI requests from auth, at
// device-code login and on every refresh. One definition keeps the two
// paths identical: a refresh that silently narrowed scopes would strand
// commands until the next full login.
//
// No sandbox scopes appear here. Cella issues its own token from this
// identity and decides what that token may carry, so requesting *:sandbox
// from auth would grant nothing Cella reads. Auth is a standard OIDC
// provider for the sandbox surface: it says who the caller is.
//
// The agents scopes stay: topos still gates on scopes auth issues.
const LoginScopes = "openid email profile offline_access run:agents read:agents write:agents"

// ErrActorAudienceMismatch signals the legacy auth behaviour where a
// device token stamped with sandboxd's audience is rejected on
// /actor-tokens; callers fall back to using the device token directly.
var ErrActorAudienceMismatch = errors.New("actor-tokens: audience mismatch")

// InferAuthURL maps a sandboxd URL like https://cella.latere.ai to the
// auth base https://auth.latere.ai. Falls back to a sane default for
// the public deployment if the API URL isn't a known shape.
func InferAuthURL(apiURL string) string {
	if apiURL == "" {
		return "https://auth.latere.ai"
	}
	if u, err := url.Parse(apiURL); err == nil && u.Host != "" {
		// Replace the leading host label.
		if _, rest, ok := strings.Cut(u.Host, "."); ok {
			u.Host = "auth." + rest
			u.Path = ""
			return u.String()
		}
	}
	return "https://auth.latere.ai"
}

// MintActorToken POSTs {authBase}/actor-tokens with the given audience
// and TTL, presenting bearer, and returns the minted actor_token. The
// actor token inherits the bearer's scopes and is issued by auth, so it
// is accepted by any service that trusts the auth issuer (sandboxd for
// audience "sandboxd", Lux for "lux.latere.ai" — Lux does not check the
// audience but the short TTL bounds a leaked export).
func MintActorToken(ctx context.Context, httpc *http.Client, authBase, bearer, audience string, ttlSeconds int) (string, error) {
	token, _, err := MintActorTokenWithLifetime(ctx, httpc, authBase, bearer, audience, ttlSeconds)
	return token, err
}

// MintActorTokenWithLifetime also returns auth's expires_in value in seconds.
// A missing or nonpositive lifetime must not be presented as a known expiry;
// the requested TTL may differ from the lifetime auth actually grants.
func MintActorTokenWithLifetime(ctx context.Context, httpc *http.Client, authBase, bearer, audience string, ttlSeconds int) (string, int64, error) {
	body, err := json.Marshal(map[string]any{"audience": audience, "ttl_seconds": ttlSeconds})
	if err != nil {
		return "", 0, fmt.Errorf("encode actor-token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBase+"/actor-tokens", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := doTokenRequest(httpc, req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		if resp.StatusCode == http.StatusUnauthorized && strings.Contains(string(b), "audience mismatch") {
			return "", 0, ErrActorAudienceMismatch
		}
		return "", 0, fmt.Errorf("actor-tokens %d: %s", resp.StatusCode, b)
	}
	var actor struct {
		ActorToken string `json:"actor_token"`
		ExpiresIn  int64  `json:"expires_in"`
	}
	if err := decodeJSONResponse(resp.Body, &actor); err != nil {
		return "", 0, fmt.Errorf("actor-tokens: %w", err)
	}
	if actor.ActorToken == "" {
		return "", 0, fmt.Errorf("actor-tokens: empty response")
	}
	return actor.ActorToken, actor.ExpiresIn, nil
}

// ExchangeAtCella trades an auth-issued bearer for a cella catalog
// token at {apiBase}/v1/tokens/exchange, labeled "CLI on <hostname>".
// Cella replaces any previous row with the same label, so repeated
// exchanges rotate the credential rather than accumulating rows.
func ExchangeAtCella(ctx context.Context, httpc *http.Client, apiBase, bearer string) (string, error) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "CLI"
	}
	body, err := json.Marshal(map[string]any{"label": "CLI on " + hostname})
	if err != nil {
		return "", fmt.Errorf("encode token-exchange request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/tokens/exchange", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := doTokenRequest(httpc, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return "", fmt.Errorf("tokens/exchange %d: %s", resp.StatusCode, b)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := decodeJSONResponse(resp.Body, &out); err != nil {
		return "", fmt.Errorf("tokens/exchange: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("tokens/exchange: empty response")
	}
	return out.AccessToken, nil
}

// Token requests must retain their POST body across redirects. Copy the
// client so callers can reuse it, and retain any stricter redirect policy.
func doTokenRequest(httpc *http.Client, req *http.Request) (*http.Response, error) {
	client := *httpc
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if err := PreserveMethodOnRedirect(next, via); err != nil {
			return err
		}
		if httpc.CheckRedirect != nil {
			return httpc.CheckRedirect(next, via)
		}
		return nil
	}
	return client.Do(req)
}

// AuthClientID resolves an explicit or saved OAuth client ID, falling back to
// AUTH_CLIENT_ID and then the CLI default for credentials saved before client
// IDs were retained. A refresh or revocation must use the issuing client.
func AuthClientID(clientID string) string {
	if clientID == "" {
		clientID = os.Getenv("AUTH_CLIENT_ID")
	}
	if clientID == "" {
		clientID = "latere-cli"
	}
	return clientID
}

// RefreshAuthToken refreshes the retained auth root token with the
// full LoginScopes set and persists the result, preserving the previous
// refresh token when the response omits a new one (a common OAuth
// behaviour).
func RefreshAuthToken(ctx context.Context, authBase string, previous Token) (Token, error) {
	clientID := AuthClientID(previous.ClientID)
	client := oidc.New(oidc.Config{
		AuthURL:  authBase,
		ClientID: clientID,
		Scopes:   strings.Fields(LoginScopes),
	})
	if client == nil {
		return Token{}, errors.New("oidc: missing AuthURL or ClientID")
	}
	tok, err := client.RefreshTokenContext(ctx, previous.RefreshToken)
	if err != nil {
		return Token{}, err
	}
	out := Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ClientID:     clientID,
		TokenType:    "Bearer",
		ExpiresAt:    tok.Expiry,
		IssuedAt:     time.Now().UTC(),
	}
	if out.RefreshToken == "" {
		out.RefreshToken = previous.RefreshToken
	}
	_ = SaveAuthToken(out) // best-effort; the in-memory token still works this run
	return out, nil
}
