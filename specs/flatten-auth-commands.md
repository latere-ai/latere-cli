---
title: Flatten auth commands to top-level verbs
status: drafted
depends_on: []
affects:
  - internal/commands/auth.go
  - internal/commands/root.go
  - internal/commands/git_credential.go
  - README.md
effort: small
created: 2026-07-12
updated: 2026-07-12
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

## Non-goals

- Changing the login flow, token storage, or org-switch semantics.
- Flattening product groups (`cella`, `lux`, `topos`, `drive`) — those are
  namespaces over distinct backends and stay grouped.
- Removing the `auth` alias now.
