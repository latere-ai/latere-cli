# latere-cli Specs

Design records for the latere CLI (`github.com/latere-ai/latere-cli`). They
explain why each surface has the shape it has, what was tried and rejected, and
the constraints that are not obvious from the code. For how to *use* the CLI,
read the [README](../README.md) and [docs/](../docs/).

## Tree

```
specs/
  001-auth-unification-migration.md  (complete    — adopts the shared device-code client and file token store)
  002-review-local-subcommand.md     (implemented — latere review; critics run through Lux and Topos)
  004-flatten-auth-commands.md       (implemented — latere login/logout/whoami/print-token/org as top-level verbs)
  005-lux-env-redesign.md            (implemented — lux env keyed by dialect and provider, with token provenance and TTL)
  .archive/
    003-arca-subcommand.md           (archived    — the storage verbs, deleted 2026-09-19 with the service they addressed)
```

## Status

Every spec in the tree has shipped:

- `001-auth-unification-migration.md`: the shared device-code client and file
  token store are in use.
- `002-review-local-subcommand.md`: `latere review` ships.
- `004-flatten-auth-commands.md`: session verbs are top-level
  (`latere login/logout/whoami/print-token/org`).
- `005-lux-env-redesign.md`: `latere lux env` takes a `--compat` dialect or a
  provider passthrough, and reports the exported token's provenance. The
  per-command lifetime knob it designed is gone; leaf id-03 of
  `latere-ai/specs/infrastructure/identity` fixed every token at the
  issuer's maximum.

One spec is archived. `003-arca-subcommand.md` designed the storage verbs;
the group and its client are deleted, and storage returns as part of the
CLI's redesign against the platform's API origin.

Two surfaces shipped without a dedicated design record: the token-lifecycle
work, which [docs/login-and-tokens.md](../docs/login-and-tokens.md) documents,
and the top-level `eval` command group, which has code but no spec or guide.
That is a recorded decision, not an open action item.

## Conventions

- The CLI talks to the auth service (login, org switch) with the saved login
  token, and reaches every product backend (Cella, Lux, Topos, Origo)
  with a token minted at auth for that one product. It does not host an HTTP
  server, does not own a cookie session, and has no frontend.
- Token storage is `~/.config/latere/auth-token.json`: the login token, and
  nothing else. A product token lives in memory for one command.
