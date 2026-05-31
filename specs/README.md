# latere-cli Specs

Design specs for the latere CLI (`github.com/latere-ai/latere-cli`).

## Tree

Active specs:

```
specs/
  auth-unification-migration.md  (planned, leaf — adopts pkg/authkit DeviceCodeClient + FileTokenStore)
```

## Dependencies

- **Upstream**: `auth/specs/auth-unification.md` (parent), specifically `auth/specs/auth-unification/authkit-device-code-and-token-store.md` and `authkit-cookie-and-env-compat.md`.
- **Downstream**: none.

## Conventions

- Frontmatter mirrors the `auth/specs/*.md` model.
- The CLI talks to auth (login, org switch) and to product backends (lux, sandbox, lectio, fs, agents) using stored Bearer tokens. It does not host an HTTP server, does not own a cookie session, and does not have a frontend.
- Token storage is `~/.config/latere/token.json` (shared with wallfacer local-mode after the unification migration).
