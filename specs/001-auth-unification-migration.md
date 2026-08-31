---
title: latere-cli — Auth Unification Migration
status: complete
depends_on:
  - latere-ai/auth/specs/.archive/043-authkit-device-code-and-token-store.md
  - latere-ai/auth/specs/.archive/042-authkit-cookie-and-env-compat.md
  - latere-ai/auth/specs/.archive/046-integration-doc-rewrite.md
affects:
  - go.mod
  - internal/commands/auth.go
  - internal/api/client.go
  - cmd/latere/
  - README.md
  - docs/
effort: small
trigger: parent auth-unification spec; latere-cli has the canonical device-code reference but on a stale pkg version with bespoke token-file handling
created: 2026-05-31
updated: 2026-05-31
author: changkun
dispatched_task_id: null
---

# latere-cli — Auth Unification Migration

## Overview

`latere-cli` already implements the canonical RFC 8628 device-code login (`internal/commands/auth.go:187-300+`) and stores Bearer tokens at `~/.config/latere/token.json`. The migration:

1. Bumps `latere.ai/x/pkg` from `v0.13.0` to the version containing `authkit.DeviceCodeClient` + `authkit.FileTokenStore`.
2. Replaces the bespoke ~100-line device flow with `DeviceCodeClient.Login(ctx)`.
3. Replaces ad-hoc token-file handling in `internal/api/client.go` with `FileTokenStore`.
4. Adds a `latere auth org switch <id>` CLI subcommand using the auth service's refresh-grant org-switch.
5. Adds `latere auth org list` and `latere auth org switch ""` for personal-context switch.
6. Updates the README and docs.

No frontend — pure CLI. No cookie or CSRF concerns.

## Current State

`go.mod` — `latere.ai/x/pkg v0.13.0` (stale; current is significantly newer).

`internal/commands/auth.go`:
- L32 — token-file path doc reference.
- L144 — `--token` flag.
- L156-187 — `saveAndVerify` writes the token file directly.
- L187-300+ — bespoke device-code flow: `oidc.New(...)`, `client.DeviceAuth(...)`, browser open, poll loop, save token.

`internal/api/client.go`:
- L3 — comment "client carries a Bearer token loaded from ~/.config/latere/token.json".
- L32 — `RefreshToken` field of the local Token struct.
- L279 — `req.Header.Set("Authorization", "Bearer "+c.Token)`.

No org-switch command today; users currently switch orgs via the dashboard / web UI and the CLI inherits whatever org context the token was minted with.

No CLI specs directory today; this spec creates `specs/`.

## Components

### `go.mod` bump

Update `latere.ai/x/pkg` from `v0.13.0` to the version containing `authkit.DeviceCodeClient` + `authkit.FileTokenStore` + `authkit.LoadConfigWithPrefix` + the cookie/env compat additions. First task in this migration is `go mod tidy` + `go build ./...` smoke before any code change, to surface transitive breakage from the version skew.

### Replace device-code flow with `DeviceCodeClient`

`internal/commands/auth.go:187-300+` collapses to:

```go
oidcClient := oidc.New(oidc.LoadConfig())
storePath, err := authkit.DefaultFileTokenStorePath()
if err != nil { return err }
store, err := authkit.NewFileTokenStore(storePath)
if err != nil { return err }

dcc := authkit.NewDeviceCodeClient(oidcClient, store)
dcc.ExtraParams = url.Values{} // optional org_id if --org flag
if err := dcc.Login(ctx); err != nil { return err }
fmt.Println("Signed in.")
```

Delete the bespoke browser-opener, poll loop, and token-write code.

### `--token` paste mode

Preserved. After bump:

```go
if token != "" {
    return store.Save(&oauth2.Token{
        AccessToken: token,
        TokenType: "Bearer",
        Expiry: time.Now().Add(1 * time.Hour), // unknown; caller refreshes on 401
    })
}
```

Note: `--token` accepts an access token without a refresh token; the CLI can't refresh it. On 401, it prompts the user to run `latere auth login` again.

### Replace ad-hoc token-file handling in `internal/api/client.go`

Replace direct file reads with `FileTokenStore.Load()`:

```go
store, _ := authkit.NewFileTokenStore(storePath)
tok, err := store.Load()
if err != nil { return err }
if tok == nil { return errNotSignedIn }
req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
```

If the access token is expired and a refresh token is present, refresh via `oidcClient.RefreshTokenContext(ctx, tok)` and persist via `store.Save`. Surface a clear error on refresh failure.

### `latere auth org list`

New subcommand:

```go
oidcClient := oidc.New(oidc.LoadConfig())
tok, _ := store.Load()
orgs, err := oidcClient.FetchOrgs(ctx, tok.AccessToken)
// print table
```

### `latere auth org switch <id>`

New subcommand using the auth service's refresh-grant org-switch (verify it is implemented before relying on it):

```go
tok, _ := store.Load()
newTok, err := oidcClient.RefreshTokenWithExtra(ctx, tok, url.Values{"org_id": {orgID}})
if err != nil { return err }
return store.Save(newTok)
```

Personal context: `latere auth org switch ""` (or `latere auth org switch --personal`).

If the refresh-grant org-switch is not yet implemented, fall back to forcing the user through `latere auth login --org <id>` (a re-auth via device-code with `org_id` in extra params).

### `oidc.Client.RefreshTokenWithExtra` (verify exists or add)

`pkg/oidc/oidc.go:384` provides `RefreshToken(r, token)`; check whether it supports an `extra url.Values` for `org_id`. If not, the migration may need a small additive method `RefreshTokenWithExtra(ctx, tok, extra)`. Verify in implementation and update the parent spec if so.

### Env prefix

Drop any bespoke env reader if present; call `oidc.LoadConfig()` (no prefix needed — the CLI was already on `AUTH_*`).

### Docs

- `README.md`:
  - Rewrite the "Authentication" section around `latere auth login` (device-code) + `latere auth org list` / `switch`.
  - Document the `~/.config/latere/token.json` file format (cross-link to the auth service's integration guide).
  - Note that wallfacer local-mode shares the same token file.
- `docs/*` (if any): update.
- `specs/README.md` (new) — index file for the new `specs/` directory.

## Sequencing

1. `go mod tidy` + `go build ./...` smoke after pkg version bump. Address transitive breakage.
2. Replace bespoke device flow with `DeviceCodeClient.Login`.
3. Replace ad-hoc token-file with `FileTokenStore`.
4. Add `latere auth org list` and `latere auth org switch <id>` commands.
5. Test `latere auth login` end-to-end against staging auth.
6. Test `latere auth org switch` end-to-end.
7. Update README.md and docs/.
8. Tag a CLI release.

## Testing Strategy

- **Existing**: `internal/commands/auth_test.go` covers the current device flow. Update mocks to point at `DeviceCodeClient`; assertions on browser-open, poll loop, file-save should pass post-migration (behavior preserved).
- **New**: test `latere auth org list` and `latere auth org switch` against a stub auth server.
- **Manual**: full E2E against staging — `latere auth login` → `latere auth org list` → `latere auth org switch <id>` → `latere -- something --requiring-auth` succeeds.

## Rollback Plan

The pkg bump touches `go.mod` + the two auth files. Revert as a single commit if the new device flow regresses. The `org list` / `org switch` commands are additive; rollback is removing them.

## Risks

- **`pkg v0.13.0` → current is a big jump**. Transitive deps (oauth2, etc.) may have moved. The `go mod tidy` smoke is the first task.
- **`RefreshTokenWithExtra` may not exist** in `pkg/oidc`. If not, this spec either adds it (small additive method) or falls back to forcing re-auth on org switch. Decide during implementation.
- **`--token` access-only mode lacks refresh**. Document that the CLI cannot refresh paste-mode tokens; users must rerun `latere auth login` when the token expires.
- **Shared token file with wallfacer local-mode**: per parent spec, both write to `~/.config/latere/token.json`. The CLI must record which issuer the token came from and refuse to refresh against a different issuer.
- **Refresh-grant org-switch dependency**: the auth service's design for it is marked complete; verify the endpoint is live in production before shipping `latere auth org switch`.
