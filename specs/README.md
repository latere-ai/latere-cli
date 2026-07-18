# latere-cli Specs

Design specs for the latere CLI (`github.com/latere-ai/latere-cli`).

## Tree

```
specs/
  review-local-subcommand.md     (implemented — latere review local Cobra subcommand; critics via Lux/topos)
  auth-unification-migration.md  (complete, leaf — adopts pkg/authkit DeviceCodeClient + FileTokenStore)
  flatten-auth-commands.md       (implemented — latere login/logout/whoami/print-token/org as top-level verbs)
  lux-env-redesign.md            (implemented — lux env keyed by route, token provenance/TTL, folds lux token)
  drive-subcommand.md            (implemented — latere drive: 8 orthogonal file-plane verbs over Drive /api/v1)
```

## Completed (archive candidates)

Every spec in the tree has shipped. `auth-unification-migration.md`
carries `status: complete`; the other four carry `status: implemented`.
All five are archive candidates:

- `auth-unification-migration.md`: authkit DeviceCodeClient + FileTokenStore in use.
- `review-local-subcommand.md`: `latere review` ships.
- `flatten-auth-commands.md`: session verbs are top-level (`latere login/logout/whoami/print-token/org`).
- `lux-env-redesign.md`: `latere lux env` is keyed by route with token provenance/TTL.
- `drive-subcommand.md`: `latere drive` file-plane verbs ship over Drive `/api/v1`.

## Recorded decisions

- Spec-parity gap: the if-11 token-lifecycle work and the top-level
  `eval` command group shipped without a dedicated design spec
  (`docs/login-and-tokens.md` documents the token lifecycle; `eval` has
  code but no spec or doc). This is a recorded decision, tracked in the
  cross-repo alignment audit (`cross-repo-alignment-2026-07.md`,
  latere-cli section), not an open action item.

## Dependencies

- **Upstream**: `auth/specs/auth-unification.md` (parent), specifically `auth/specs/auth-unification/authkit-device-code-and-token-store.md` and `authkit-cookie-and-env-compat.md`.
- **Downstream**: none.

## Conventions

- Frontmatter mirrors the `auth/specs/*.md` model.
- The CLI talks to auth (login, org switch) and to product backends (lux, sandbox, lectio, fs, agents) using stored Bearer tokens. It does not host an HTTP server, does not own a cookie session, and does not have a frontend.
- Token storage is `~/.config/latere/token.json` (shared with wallfacer local-mode after the unification migration).
