---
title: Redesign lux env around routes and token lifetime
status: implemented
depends_on: []
affects:
  - internal/commands/lux.go
  - docs/lux.md
effort: small
created: 2026-07-12
updated: 2026-07-12
author: changkun
dispatched_task_id: null
---

# Redesign lux env around routes and token lifetime

## Overview

`lux env` is the keyless SDK-enablement path and worth keeping, but its
design conflates three things behind one `--provider` flag: which Lux
*route* to point at, which SDK *dialect* the exports serve, and *which
token* gets embedded. This spec unbundles them.

## What is wrong today

1. **`--provider` is the wrong axis.** OpenAI, OpenRouter, and local
   routes all speak the OpenAI dialect and emit the same two variables
   with different base URLs; Anthropic is the odd one out; Gemini is a
   permanent error. The flag looks like "choose your vendor" but
   actually chooses a route-and-dialect pair, and the dialect is
   derivable from the route.
2. **Token story is invisible.** The exported credential is whatever
   `luxIdentityBearer` resolves (a passthrough token from `--token` or
   `$LATERE_LUX_TOKEN`, or the refreshed identity token), and the command
   never says which one it embedded or when it dies. An identity token
   expires with the login session; users discover this as a mystery 401
   in their SDK, far from the `eval` that planted it.
3. **`lux token` overlaps.** It mints/prints a bearer; `env` prints the
   same bearer wrapped in exports. Two commands, one job.

## Design

One verb, route as the positional argument, dialect inferred:

    latere lux env [route]        # openai (default) | openrouter | anthropic | local

- Emits the exports for the route's dialect: OpenAI-compat routes →
  `OPENAI_BASE_URL` + `OPENAI_API_KEY`; anthropic → `ANTHROPIC_BASE_URL`
  + `ANTHROPIC_AUTH_TOKEN`. Gemini stays unsupported with the same
  actionable error.
- **Says what it embedded** on stderr (stdout stays eval-clean):
  `# identity token, expires 2026-07-12T22:10Z — re-run after expiry`
  or `# passthrough token (--token or $LATERE_LUX_TOKEN)`.
- `--ttl <duration>` mints a short-lived `aud=lux.latere.ai` actor token
  instead of the identity token (CI: bound blast radius, no refresh
  file). Reuses `mintActorToken`.
- `lux token` becomes a hidden deprecated alias for `lux env --raw`
  (bare token on stdout, no exports), collapsing the overlap the same
  way rates/providers folded into models.
- `--provider` stays as a hidden flag alias for the positional for one
  release.

## Testing Strategy

Follows lux_test.go conventions: exports per route (table), stderr
provenance note per token source, `--ttl` mint path against a fake
auth `/actor-tokens`, `--raw` bare output, hidden-alias checks.

## Outcome

Shipped same day, directly on main. Drift: minimal — one deliberate
deviation: `lux token` stays a hidden alias with its old behavior
(identity bearer to stdout) rather than literally delegating to
`env --raw`, so its output shape cannot change under scripts; `--raw`
additionally prints the provenance note on stderr. `--provider` remains
as a hidden flag alias. Verified live: positional routes emit the right
dialect exports, and stderr reports
`# identity token, expires 2026-07-12T20:27Z — re-run after expiry`
against the real login. Six new tests cover routes, eval-clean stdout,
`--ttl` minting (ttl_seconds forwarded), `--raw`, the expiry note, and
the hidden alias.

## Superseded, in part (2026-07-20)

This spec keyed `env` on the **route**, treating "which provider" and
"which dialect" as one axis with `openai` as the default. They are two
axes: the compat surfaces (`/compat/openai/v1`, `/compat/anthropic`)
speak a dialect while reaching *any* model Lux routes, and this design
had no way to name them.

`--compat [openai|anthropic|lux]` now selects the dialect, the
positional argument stays a provider passthrough, and the two cannot be
combined (env vars carry a base URL, not a model, so a provider has
nowhere to sit on a compat route). The `openai` default is gone: it
read as "the way to reach Lux" while silently pinning the caller to one
vendor's models, so a bare `lux env` now errors and names both axes.

Everything else here stands: token provenance, `--ttl`, `--raw`, and
the hidden `--provider` alias are unchanged.

## Non-goals

- A local credential-injecting proxy (would fix expiry properly but is
  a different weight class; revisit if expiry keeps biting).
- Changing how Lux validates tokens.
