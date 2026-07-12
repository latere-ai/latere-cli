# latere-cli Specs

Design specs for the latere CLI (`github.com/latere-ai/latere-cli`).

## Tree

Active specs:

```
specs/
  review-local-subcommand.md     (implemented — latere review local Cobra subcommand; critics via Lux/topos)
  auth-unification-migration.md  (planned, leaf — adopts pkg/authkit DeviceCodeClient + FileTokenStore)
  flatten-auth-commands.md       (implemented — latere login/logout/whoami/print-token/org as top-level verbs)
  drive-subcommand.md            (drafted — latere drive: 8 orthogonal file-plane verbs; depends on flatten-auth-commands)
```

## Dependencies

- **Upstream**: `auth/specs/auth-unification.md` (parent), specifically `auth/specs/auth-unification/authkit-device-code-and-token-store.md` and `authkit-cookie-and-env-compat.md`.
- **Downstream**: none.

## Conventions

- Frontmatter mirrors the `auth/specs/*.md` model.
- The CLI talks to auth (login, org switch) and to product backends (lux, sandbox, lectio, fs, agents) using stored Bearer tokens. It does not host an HTTP server, does not own a cookie session, and does not have a frontend.
- Token storage is `~/.config/latere/token.json` (shared with wallfacer local-mode after the unification migration).
