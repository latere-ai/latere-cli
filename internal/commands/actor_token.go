// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"latere.ai/x/pkg/otel"

	"github.com/latere-ai/latere-cli/internal/api"
)

// actorTokenTTL bounds the token a product receives, in seconds. A git
// exchange or a file command completes in seconds, so five minutes covers
// it and limits the window of a value that leaks through git's own
// credential store or a trace.
const actorTokenTTL = 300

// actorCredentialToken resolves the bearer presented to a product that
// enforces its own audience: a short-lived actor token bound to audience,
// minted at auth with the retained root token (refreshed when expired via
// the same authIdentityToken path `latere lux` uses). The root token itself
// is never presented: its audience is auth, sandboxd and toposd, and the
// product rejects it. Falls back to token.json only when the auth file is
// absent, as it is after --token paste login; that case returns
// api.ErrNoToken when token.json is empty too. Existing auth failures must
// not change identity, so they are returned, not masked by the fallback.
// name is what an error calls the product.
func actorCredentialToken(ctx context.Context, authURL, audience, name string) (string, error) {
	access, authBase, err := authIdentityToken(ctx, "", authURL)
	if err == nil {
		return mintActorToken(ctx, authBase, access, audience, name)
	}
	if !errors.Is(err, api.ErrNoToken) {
		return "", err
	}
	if tok, lerr := api.LoadToken(""); lerr == nil && tok.AccessToken != "" {
		return tok.AccessToken, nil
	}
	return "", err
}

// mintActorToken exchanges the root token for an actor token bound to
// audience. Any failure, auth unreachable included, is an error the caller
// decides how to surface: the git helper stays silent so git prompts, the
// file commands report it.
func mintActorToken(ctx context.Context, authBase, access, audience, name string) (string, error) {
	httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil)}
	actor, err := api.MintActorToken(ctx, httpc, authBase, access, audience, actorTokenTTL)
	if err != nil {
		return "", fmt.Errorf("mint %s token: %w; if this persists run `latere login`", name, err)
	}
	return actor, nil
}
