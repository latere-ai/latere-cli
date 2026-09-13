// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"latere.ai/x/pkg/authkit/oidc"
	"latere.ai/x/pkg/otel"
)

// LoginScopes is the single scope set the CLI requests from auth, at
// device-code login and on every refresh. One definition keeps the two
// paths identical: a refresh that silently narrowed scopes would strand
// commands until the next full login.
//
// The scopes are the person's, not a product's. Every product call
// presents an actor token minted from this login, and the actor token
// inherits these scopes with one product's audience stamped on it.
const LoginScopes = "openid email profile offline_access run:agents read:agents write:agents"

// DefaultAuthURL is the issuer of the public deployment.
const DefaultAuthURL = "https://auth.latere.ai"

// actorMintTimeout bounds one call to the issuer. A mint is one round
// trip; a command that hangs on an unreachable issuer helps nobody.
const actorMintTimeout = 15 * time.Second

// refreshMargin is how close to expiry the login token is refreshed
// before it is used to mint. It covers a slow round trip to the issuer.
const refreshMargin = 60 * time.Second

// InferAuthURL maps a product URL like https://cella.latere.ai to the
// issuer base https://auth.latere.ai. Falls back to the public issuer if
// the product URL isn't a known shape: a bare single-label host, or an IP
// literal, whose dots separate octets rather than DNS labels and so have
// no leading label to replace.
func InferAuthURL(apiURL string) string {
	if apiURL == "" {
		return DefaultAuthURL
	}
	if u, err := url.Parse(apiURL); err == nil && u.Host != "" && net.ParseIP(u.Hostname()) == nil {
		// Replace the leading host label.
		if _, rest, ok := strings.Cut(u.Host, "."); ok {
			u.Host = "auth." + rest
			u.Path = ""
			return u.String()
		}
	}
	return DefaultAuthURL
}

// ResolveAuthURL resolves the issuer base: an explicit value wins, then
// $AUTH_URL, then the issuer inferred from the product URL.
func ResolveAuthURL(apiURL, authURL string) string {
	if authURL == "" {
		authURL = os.Getenv("AUTH_URL")
	}
	if authURL == "" {
		authURL = InferAuthURL(apiURL)
	}
	return strings.TrimRight(authURL, "/")
}

// ActorToken returns the bearer a product receives: a token addressed to
// audience alone, minted at the issuer for the person the saved login
// proves, and valid for oidc.ActorTokenLifetime. authURL is the issuer
// base; an empty value resolves $AUTH_URL and then the public issuer.
//
// This is the CLI's one mint. Every product command reaches a product
// through it, so the login token stays on the wire to the issuer alone:
// it is addressed to the issuer, and a product refuses it.
func ActorToken(ctx context.Context, authURL, audience string) (string, time.Time, error) {
	access, authBase, err := LoginToken(ctx, authURL)
	if err != nil {
		return "", time.Time{}, err
	}
	mintCtx, cancel := context.WithTimeout(ctx, actorMintTimeout)
	defer cancel()
	token, expiry, err := oidc.MintActorToken(mintCtx, authBase, access, audience)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w; if this persists run `latere login`", err)
	}
	return token, expiry, nil
}

// LoginToken returns the saved login token's access token and the issuer
// base it belongs to, refreshing the saved token when it is within
// refreshMargin of a known expiry. It is what an actor token is minted
// with and what the issuer's own endpoints are called with; it is never
// presented to a product.
func LoginToken(ctx context.Context, authURL string) (access, authBase string, err error) {
	tok, err := LoadAuthToken()
	if err != nil {
		if errors.Is(err, ErrNoToken) {
			return "", "", err
		}
		// A saved login that exists but cannot be read is only repaired
		// by signing in again, so carry the hint every other failure has.
		return "", "", fmt.Errorf("read saved login: %w; run `latere login`", err)
	}
	authBase = ResolveAuthURL("", authURL)
	access = tok.AccessToken
	// A known expiry has two thresholds: lapsed, which no mint can use,
	// and due, which is early enough that the mint would race it. A token
	// with no refresh grant is only unusable once it has actually lapsed.
	lapsed := !tok.ExpiresAt.IsZero() && !time.Now().Before(tok.ExpiresAt)
	due := !tok.ExpiresAt.IsZero() && time.Now().After(tok.ExpiresAt.Add(-refreshMargin))
	switch {
	case tok.RefreshToken == "" && lapsed:
		return "", "", errors.New("the saved login expired and carries no refresh token; run `latere login`")
	case tok.RefreshToken != "" && due:
		refreshed, rerr := RefreshAuthToken(ctx, authBase, tok)
		if rerr != nil {
			return "", "", fmt.Errorf("the saved login expired and refresh failed (%w); run `latere login`", rerr)
		}
		access = refreshed.AccessToken
	}
	if access == "" {
		return "", "", errors.New("the saved login carries no access token; run `latere login`")
	}
	return access, authBase, nil
}

// Token requests must retain their POST body across redirects. Copy the
// client so callers can reuse it, and retain any stricter redirect policy.
func tokenHTTPClient(httpc *http.Client) *http.Client {
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
	return &client
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

// RefreshAuthToken refreshes the saved login token with the full
// LoginScopes set and persists the result, preserving the previous
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
	// oauth2 selects its token-exchange client from the context. Preserve
	// custom transports and timeouts while enforcing the token redirect policy.
	httpc, _ := ctx.Value(oauth2.HTTPClient).(*http.Client)
	if httpc == nil {
		httpc = otel.HTTPClient()
	}
	refreshHTTP := tokenHTTPClient(httpc)
	transport := refreshHTTP.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	refreshHTTP.Transport = authRefreshTransport{base: transport}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, refreshHTTP)
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
	if err := SaveAuthToken(out); err != nil {
		return Token{}, fmt.Errorf("save refreshed login token: %w", err)
	}
	return out, nil
}
