// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"latere.ai/x/pkg/otel"
	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/signin"
)

// userAgent is the product the CLI names itself as, before the SDK's own
// token in every request's User-Agent.
const userAgent = "latere-cli"

// actorEarly is how long before an actor token expires a product command
// mints the next one, so a request does not reach a product with a token
// that lapses on the way.
const actorEarly = 30 * time.Second

// actorTokens returns the actor tokens for audience minted from the saved
// login at the issuer authURL resolves, each held until shortly before it
// expires. Concurrent calls share one mint. A failure to mint reaches the
// command as a *latere.TokenError, before any request is sent. The source
// can drop its token, so a product's 401 is sent once more with a token
// minted again, which rides out a token revoked or expired early.
func actorTokens(authURL, audience string) latere.TokenSource {
	return latere.ReuseToken(latere.TokenSourceFunc(func(ctx context.Context) (latere.Token, error) {
		t, expiry, err := ActorToken(ctx, authURL, audience)
		if err != nil {
			return latere.Token{}, err
		}
		return latere.Token{Value: t, Expiry: expiry}, nil
	}), actorEarly)
}

// HTTPClient is the client the CLI's SDK configurations send with: traced,
// and refusing a redirect that changes the method. It has no Timeout of
// its own, so a followed stream is not cut off; a Config bounds unary
// calls with latere.WithTimeout.
func HTTPClient() *http.Client {
	return &http.Client{Transport: otel.Transport(nil), CheckRedirect: PreserveMethodOnRedirect}
}

// Issuer is the SDK's configuration for the issuer at authBase, the
// identity service the saved login belongs to, as the client clientID that
// issued the login (AuthClientID resolves an empty one): the CLI's traced
// HTTP client, which refuses a redirect that changes the method. The calls
// made with it are bounded by their context.
func Issuer(authBase, clientID string) *signin.Config {
	return &signin.Config{
		ClientID:   AuthClientID(clientID),
		Issuer:     authBase,
		Scopes:     strings.Fields(LoginScopes),
		HTTPClient: HTTPClient(),
	}
}

// Product is how a product command reaches one capability of the API
// through the SDK.
type Product struct {
	// BaseURL is the origin the SDK's capability packages build their
	// paths on, such as https://api.latere.ai.
	BaseURL string
	// AuthURL is the issuer base the actor tokens are minted at; empty
	// resolves $AUTH_URL and then the issuer inferred from BaseURL.
	AuthURL string
	// Audience is the aud claim of the actor tokens, such as "insula".
	Audience string
	// Token, when set, is presented as given instead of an actor token
	// minted from the saved login, for a token the person exported.
	Token string
	// Timeout bounds every unary call, its retries included; zero keeps
	// the SDK's default.
	Timeout time.Duration
}

// Config builds the SDK configuration for p: the given token, or actor
// tokens for p.Audience minted from the saved login and minted again as
// they lapse, so a long command keeps working; the CLI's traced HTTP
// client; and the CLI's User-Agent. The SDK's retries stay on: a request
// that is safe to repeat is sent again after a network failure or a
// temporary refusal (429, 5xx), so a command rides out a brief outage
// instead of failing on it.
func (p Product) Config() (*latere.Config, error) {
	tokens := actorTokens(ResolveAuthURL(p.BaseURL, p.AuthURL), p.Audience)
	if p.Token != "" {
		tokens = latere.StaticToken(p.Token)
	}
	opts := []latere.Option{
		latere.WithTokenSource(tokens),
		latere.WithBaseURL(p.BaseURL),
		latere.WithHTTPClient(HTTPClient()),
		latere.WithUserAgent(userAgent),
	}
	if p.Timeout > 0 {
		opts = append(opts, latere.WithTimeout(p.Timeout))
	}
	cfg, err := latere.NewConfig(opts...)
	if err != nil {
		return nil, fmt.Errorf("configure the API client: %w", err)
	}
	return cfg, nil
}

// RefusalText is the sentence of a refusal: the API's message, or the text
// of an answer that is not the API's envelope, such as a proxy's page or an
// issuer's plain-text refusal. The SDK names such an answer by its status's
// text and keeps its bytes in Body, so the text comes from Body, trimmed and
// cut to maxRefusalText bytes on a character boundary. An empty answer has
// none.
func RefusalText(e *latere.Error) string {
	if e.Code != "" || e.Message != http.StatusText(e.StatusCode) {
		return e.Message
	}
	text := strings.TrimSpace(string(e.Body))
	if strings.HasPrefix(text, "{") {
		// The API's envelope whose message is the status's own text.
		return e.Message
	}
	if len(text) > maxRefusalText {
		cut := maxRefusalText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}

// maxRefusalText bounds the text a refusal without the API's envelope shows.
const maxRefusalText = 200
