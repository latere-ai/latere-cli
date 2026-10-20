// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"latere.ai/x/pkg/otel"
	"latere.ai/x/sdk-go/latere"
)

// userAgent is the product the CLI names itself as, before the SDK's own
// token in every request's User-Agent.
const userAgent = "latere-cli"

// LoginTokens returns the saved login as the source of a person's sign-in
// token: each call reads the login `latere login` saved, refreshing and
// saving it when it is about to expire, as LoginToken does. authURL is the
// issuer base; an empty value resolves $AUTH_URL and then the public
// issuer. The token's expiry is left unknown, so the SDK asks again for
// every actor token it mints, and the refresh stays LoginToken's.
func LoginTokens(authURL string) latere.TokenSource {
	return latere.TokenSourceFunc(func(ctx context.Context) (latere.Token, error) {
		access, _, err := LoginToken(ctx, authURL)
		if err != nil {
			return latere.Token{}, err
		}
		return latere.Token{Value: access}, nil
	})
}

// HTTPClient is the client the CLI's SDK configurations send with: traced,
// and refusing a redirect that changes the method. It has no Timeout of
// its own, so a followed stream is not cut off; a Config bounds unary
// calls with latere.WithTimeout.
func HTTPClient() *http.Client {
	return &http.Client{Transport: otel.Transport(nil), CheckRedirect: PreserveMethodOnRedirect}
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
// client; and the CLI's User-Agent.
func (p Product) Config() (*latere.Config, error) {
	var tokens latere.TokenSource
	if p.Token != "" {
		tokens = latere.StaticToken(p.Token)
	} else {
		actor, err := latere.NewActorTokens(LoginTokens(p.AuthURL),
			latere.WithIssuer(ResolveAuthURL(p.BaseURL, p.AuthURL)), latere.WithAudience(p.Audience))
		if err != nil {
			return nil, err
		}
		tokens = actor
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
