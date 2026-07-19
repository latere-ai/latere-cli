---
title: Flatten auth commands to top-level verbs
status: implemented
depends_on: []
affects:
  - internal/commands/auth.go
  - internal/commands/root.go
  - internal/commands/git_credential.go
  - README.md
effort: small
created: 2026-07-12
updated: 2026-07-12
implemented: 2026-07-12
author: changkun
dispatched_task_id: null
---

# Flatten auth commands to top-level verbs

## Overview

`latere auth login` is one level deeper than it needs to be for the single
most common command a user types. Session verbs move to the top level —
`latere login`, `latere logout`, `latere whoami`, `latere print-token`,
`latere org` — making the root namespace read as actions, not an
implementation taxonomy. The `latere auth` group survives as a hidden
back-compat alias.

## Current State

`internal/commands/auth.go`: `newAuthCmd()` groups `newAuthLoginCmd`,
`newAuthWhoamiCmd`, `newAuthPrintTokenCmd`, `newAuthLogoutCmd`, and
`newAuthOrgCmd` (with `switch`). Registered in root.go alongside `cella`,
`topos`, `lux`, `review`, `git-credential`, `upgrade`, `completion`. Several
user-facing error strings across the repo say `run latere auth login ...`
(review.go, lux.go, git_credential.go).

## Design

### Top-level verbs

Each existing child factory is registered directly on root:

| Command | Behavior |
|---|---|
| `latere login` | Device-code login; keeps `--no-git`, `--api-url`, etc. unchanged |
| `latere logout` | Clear stored tokens |
| `latere whoami` | Show identity from the stored token |
| `latere print-token` | Bare token to stdout (machine output; add to `skipUpdateCheck`) |
| `latere org` | No args: print the active org. `latere org <uuid>`: switch. `--personal`: switch to personal |

`latere org` replaces `auth org switch <uuid>`: the verb collapses — show
when no argument, switch when given one (`cobra.MaximumNArgs(1)`).

### Back-compat alias

`newAuthCmd()` stays registered but with `Hidden: true`; its children are
built from the same factories, so behavior cannot drift. No deprecation
noise on stderr — scripts using `latere auth print-token` keep working
silently. Remove the alias in a later major version.

Cobra note: a `*cobra.Command` instance cannot have two parents, so root and
the hidden `auth` group each call the shared factories to get separate
instances.

### Message sweep

All user-facing hints change to the flat form: `run latere login` (grep for
`latere auth login` across internal/ and README.md). `skipUpdateCheck`
(root.go:66) gains `print-token` (bare-token stdout must stay clean).

## Landscape Impact

`latere auth <verb>` appears in ~50 files across `../` (survey 2026-07-12;
dominant forms: `login` ×45 incl. `--token`/`--org-id`, `print-token` ×16,
`whoami` ×4, `logout` ×2). The hidden alias means **nothing breaks at
flatten time**; the alias cannot be removed until the sweep below lands.

By class, in migration order:

1. **This repo** — commands, hints, README (part of this spec).
2. **Executable dependency** — `sandbox/test/e2e-cli-credential-smoke.sh`
   invokes `latere auth login` (the only script/CI caller found). Switch to
   `latere login` once a flattened CLI release is out.
3. **Server-emitted remediation** — `sandbox/api/errors.go:94` tells API
   callers to `Run \`latere auth login\``. Cella deploy must trail the CLI
   release (old CLIs still understand the alias, so ordering is soft).
4. **Product frontends (user-visible copy, en+zh)** — sandbox frontend
   (ConsoleScreen, DetailPane, HeroDemo, i18n), drive frontend
   (`onboarding.ts` + test, `CloneUrlBox.vue`), agents frontend (review
   content).
5. **Docs & marketing** — sandbox `docs/cella/*` (en+zh) + deploy docs, lux
   `docs/lux/*`, drive README, auth `INTEGRATION.md`, wallfacer docs +
   `internal/cli/auth.go` help text, latere-ai site (`developers.html`,
   introducing-drive blog en+zh), `../specs/products/cli.md`.
6. **Not affected** — `pkg/oidclogin` ("latere auth" there names the auth
   *service*, not the CLI); archived specs stay as written; code comments
   (auth `device.go:33`) are cosmetic.

Classes 2-5 are per-repo follow-up commits in their own repos, not part of
this spec's implementation; this spec is done when class 1 ships and the
alias covers the rest.

## Testing Strategy

- `TestTopLevelVerbsRegisteredInRoot`: `login`, `logout`, `whoami`,
  `print-token`, `org` present in `NewRoot("test").Commands()`.
- `TestAuthAliasHiddenButFunctional`: `auth` is `Hidden` and `latere auth
  login --help` still resolves.
- `TestOrgShowAndSwitch`: no-arg prints active org; arg triggers the
  refresh-grant switch (httptest fake auth `/token`, pattern from
  auth_test.go).
- `TestSkipUpdateCheckForPrintToken`.
- Help-text test asserting root `--help` lists the verbs and not `auth`.
- Grep-guard test (or just the sweep): no remaining `latere auth login`
  string in user-facing output.

## Outcome

Shipped directly on main, same day as drafting. Drift: minimal — all spec
items satisfied. Commits: `3aa5434` (flatten + org verb + tests), `4630c93`
(hint sweep in errors/help/comments + their tests), `5ab0aae` (README and
product guides).

**What shipped.** `latere login/logout/whoami/print-token/org` registered on
root; `auth` (and `auth org switch`) hidden but functional from the same
factories; `switchOrgContext`/`showOrgContext` extracted so both paths share
one implementation; `print-token` added to `skipUpdateCheck`; every
user-facing `latere auth <verb>` string in code, tests, README, and docs
moved to the flat form. Seven new tests in
`internal/commands/flatten_auth_test.go`; full `make build` gate green.
Verified live: `latere org` prints the active context, root help hides
`auth`, the alias still resolves.

**Deviations.** Test names differ from the spec sketch
(`TestTopLevelSessionVerbsRegisteredInRoot`; `TestOrgShowAndSwitch` split
into four focused tests). The grep-guard for leftover `latere auth login`
strings was done as a one-time sweep, not a standing test — a test would
false-positive on the alias's own doc comments.

**Decisions.** `latere org` prints the bare context (`personal` or the org
UUID) to stdout so it is scriptable; org id + `--personal` together is an
explicit error rather than silently preferring one; the alias prints no
deprecation notice (scripts' stderr stays clean).

**Follow-ups.** Landscape classes 2–5 (sandbox smoke script, cella
remediation string, product frontends, cross-repo docs) land per-repo after
a release ships, per the Landscape Impact section. specs/003-drive-subcommand.md
is now unblocked.

## Non-goals

- Changing the login flow, token storage, or org-switch semantics.
- Flattening product groups (`cella`, `lux`, `topos`, `drive`) — those are
  namespaces over distinct backends and stay grouped.
- Removing the `auth` alias now.
