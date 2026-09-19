---
title: latere arca subcommand
status: implemented
depends_on:
  - 004-flatten-auth-commands.md
affects:
  - internal/commands/arca.go
  - internal/commands/root.go
  - internal/arca/client.go
  - cmd/latere/arca_e2e_test.go
  - docs/arca.md
  - README.md
effort: medium
created: 2026-07-12
updated: 2026-09-19
implemented: 2026-07-12
author: changkun
dispatched_task_id: null
---

# latere arca subcommand

## Overview

`latere arca` is the CLI face of the family's storage core. It shipped on
2026-07-12 as `latere drive` against a hosted service at a host of its own; on
2026-09-19 it became `latere arca` against Arca (`../arca`) behind the
platform's API origin, per that repository's
[019-migration-from-drive](../../arca/specs/019-migration-from-drive.md), whose
consumer table names this group. The verb set did not change.

This spec defines a **small, orthogonal** verb set: every command does one
thing, variations are flags, and the same path addressing works everywhere.

## Design principles

- **One verb, one action.** No command aliases another; no verb has a mode
  that changes what kind of thing it does.
- **Flags are modifiers, not modes.** `--version N` selects a version on any
  verb it makes sense for (`get`, `rm`, `restore`); `--permanent` hardens
  `rm`; `--trashed` widens `ls`. There is no separate `trash` or `versions`
  command group.
- **No redundant surface.** Identity is `latere whoami`; the storage core
  reads no claim for meaning and has no identity route to ask. Git is `git`
  plus the credential helper `latere login` wires. Workspaces, events, the
  administrative overview and materialize are the console's and the sandbox
  plane's, not a person's terminal verbs.
- **The CLI names no service host.** The base URL is the platform's API
  origin; the audience on the bearer is what selects Arca behind it.

## Architecture

- `internal/commands/arca.go` — the whole cobra tree, `newArcaCmd()` factory,
  registered via `root.AddCommand(newArcaCmd())` in root.go. It holds
  `arcaName`, which root.go's update-check exemption matches on, and
  `arcaAudience`, the one file in the tree that mints for that audience
  (the identity gate's `client-audiences` rule).
- `internal/arca/client.go` — a thin typed client over the origin's `/v1`.
  It mirrors `internal/api/client.go`'s conventions (Bearer header,
  `User-Agent: latere-cli`, 60s timeout, non-2xx → typed error) and decodes
  the family error envelope. It names no product: the token decides which
  service answers.
- **Bearer**: an actor token of audience `arca`, minted at the issuer from
  the saved login, five minutes, one per command. `--token` and
  `LATERE_ARCA_TOKEN` pass a bearer through for CI.
- **Base URL**: `arca.ResolveURL(flag)` — `--api-url` > `ARCA_API_URL` >
  `https://api.latere.ai`. The issuer follows the resolved origin unless
  `--auth-url` or `AUTH_URL` names one.
- `arcaName` is in `skipUpdateCheck` (root.go): `get -o -` streams file bytes
  to stdout and must stay clean.

## Command Space

Ten verbs, all under `latere arca`:

| Command | Does | API |
|---|---|---|
| `ls [prefix]` | List files under a prefix (default `files/`) | `GET /v1/files/{owner}/{prefix}?list`; `--trashed` → `GET /v1/trash` |
| `get <path>` | Download one file | `GET /v1/files/…` (follow 302) |
| `put <src> [path]` | Upload one file (default path `files/<basename>`) | `PUT /v1/files/…`, an upload session above 16 MiB |
| `mv <src> <dst>` | Move or rename within a space | `POST /v1/files/…` `{move_to}` |
| `rm <path>` | Trash a file | `DELETE /v1/files/…` |
| `restore <path>` | Undo: from trash, or to a prior version | `POST /v1/trash/restore`; with `--version N` → `POST /v1/files/…` `{restore_version}` |
| `history <path>` | List a file's versions | `GET /v1/files/…?versions` |
| `share <prefix>` | Grant access | `POST /v1/shares` for `--to`; `POST /v1/shares/links` for `--link` and `--public` |
| `shares` | List what reaches a space | `GET /v1/shares` and `GET /v1/shares/links`; `--inbox` → `GET /v1/shares/with-me` |
| `unshare <id>` | Revoke | `DELETE /v1/shares/{id}`, then `DELETE /v1/shares/links/{id}` |

Flags:

| Flag | On | Purpose |
|---|---|---|
| `--owner` | all (persistent) | Space: `me` (default) or the subject of another space |
| `--api-url`, `--auth-url`, `--token` | all (persistent) | Endpoint and bearer overrides as above |
| `--json` | all (persistent) | Machine-readable output on stdout |
| `--long` | `ls` | Size, mtime, checksum columns |
| `--trashed` | `ls` | List trashed files instead of live ones |
| `--version N` | `get`, `rm`, `restore` | Operate on version N (download it, prune it, restore it) |
| `--permanent` | `rm` | Hard-delete instead of trash (also purges an already-trashed file) |
| `-o <file\|->` | `get` | Output destination (default: basename; `-` is stdout) |
| `--if-match <etag>`, `--create-only` | `put` | Conditional overwrite, create-only (`If-None-Match: *`) |
| `--to`, `--link`, `--public`, `--permission`, `--expires` | `share` | Exactly one recipient; a token grant is read-only |
| `--inbox` | `shares` | List shares granted *to* me instead of *by* me |

Paths are plane-rooted exactly as in the API (`files/…`, `workspaces/…`); no
prefix-guessing. `put` streams a single PUT at or below 16 MiB and opens an
upload session above it (16 MiB parts, at most 1000, 4 in flight, abandon the
session on failure).

## Error Handling

- Not signed in: `not logged in; run latere login`, the same message family
  as review and lux.
- The family envelope reaches the person as `code: sentence`, then the
  developer detail, then the request id, so a failure can be quoted.
- `precondition_failed` on `put`: actionable guidance, because no route
  demands a condition and a refusal is therefore always one the caller asked
  for.
- An upload session failure abandons the session (`DELETE /v1/uploads/{id}`)
  before returning.

## Testing Strategy

Follows `internal/commands` conventions (white-box, package `commands`):

- `TestArcaCommandRegisteredInRoot`; help-text tests via `executeForHelp`.
- `TestArcaVerbSet` pins the command space, so growth is a spec change.
- Flag-default tests; `TestSkipUpdateCheckForArca`.
- `httptest.NewServer` fakes; seeded tokens via `writeAuthTokenFile`.
  `TestProductCredentialsCarryOnlyTheirOwnAudience` holds the bearer to the
  `arca` audience alone.
- `internal/arca` client tests: the error envelope, conditional headers, the
  part arithmetic, and every receipt the client refuses to accept.
- E2E over the built binary: put, ls, get, history, rm, restore, the upload
  session, the redirect policy, and each receipt rule.

## Outcome

**Shipped 2026-07-12** as `latere drive`, directly on main. Drift: minimal;
the eight-verb space landed exactly as specced, with one naming split
(`shares --inbox` instead of a flag on `share`) already in the spec table.
Commits: `19230df` (client, 87.8% coverage), `94e8e3f` (command tree and
tests), `3346fc1` (guide and README row), `438824f` (live-API fix),
`44a2fc0` (binary e2e).

**Cut over to Arca 2026-09-19**, branch `arca-cutover`, against
[arca 019](../../arca/specs/019-migration-from-drive.md). What changed for
the person using it: the group is `latere arca`; the base URL is the API
origin and the bearer's audience is `arca`; a space is a subject, and `org`,
`u-<uuid>` and `o-<uuid>` are refused before anything is sent; a share is a
grant or a token grant on two routes, with the role, team, organization and
invited-address kinds gone; `--permission` is refused on a token grant; the
flag and variables are `--api-url`, `ARCA_API_URL` and `LATERE_ARCA_TOKEN`.
Under it: one `Object` shape for every file route, the family error
envelope, `permanent=1` rather than `permanent=true`, and `--api-url` now
steering the issuer too. The quota reader is deleted; no subcommand read it.

**Found against Arca's contract.** A move receipt no longer echoes the
source, and a version restore no longer echoes the revision, so those two
receipts are held to the path and the checksum instead. A revoke cannot tell
a grant id from a token grant id, so `unshare` tries both id spaces in turn.
A token grant's address is the API path it is redeemed at; the contract
carries no address a person would open.

**Decisions.** Two security-relevant ones in the client: the bearer is
stripped on every redirect (Go only auto-strips cross-host, and presigned
URLs reject double auth), and part PUTs go bare to the object store.
`rm --permanent` falls back to a trash purge when the live file is already
gone, making it the single "make it not exist" verb. Version pruning rides
`rm --version` per the flags table.

## Non-goals

- `arca whoami` — `latere whoami` owns identity.
- Workspace lifecycle (`/v1/workspaces…`), attach, materialize and sync — the
  sandbox mount contract, driven by the sandbox plane and the console.
- Events, stars, the administrative overview — console flows; add individual
  verbs later only on demand.
- Git sugar (`clone`) and LFS — repositories live on Latere Code, where plain
  `git` works through the credential helper.
- Redeeming a share token (`GET /v1/shares/links/{token}`) — reachable
  without a bearer, so nothing in the CLI needs to wrap it.
