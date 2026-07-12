---
title: latere drive subcommand
status: implemented
depends_on:
  - flatten-auth-commands.md
affects:
  - internal/commands/drive.go
  - internal/commands/root.go
  - internal/drive/client.go
  - cmd/latere/drive_e2e_test.go
  - docs/drive.md
  - README.md
effort: medium
created: 2026-07-12
updated: 2026-07-12
implemented: 2026-07-12
author: changkun
dispatched_task_id: null
---

# latere drive subcommand

## Overview

`latere drive` is the CLI face of Latere Drive (`../drive`, live at
`https://drive.latere.ai`). Today the CLI only wires Drive's git plane
(`latere git-credential`); the file plane is reachable only through the SPA or
raw curl. This spec defines a **small, orthogonal** verb set — every command
does one thing, variations are flags, and the same path addressing works
everywhere.

## Design principles

- **One verb, one action.** No command aliases another; no verb has a mode
  that changes what kind of thing it does.
- **Flags are modifiers, not modes.** `--version N` selects a version on any
  verb it makes sense for (`get`, `rm`, `restore`); `--permanent` hardens
  `rm`; `--trashed` widens `ls`. There is no separate `trash` or `versions`
  command group.
- **No redundant surface.** Identity is `latere whoami` (auth owns identity;
  Drive validates the same JWT — see `flatten-auth-commands.md`). Git is
  `git` + the credential helper already wired by login. Admin, webhooks,
  events, and workspace lifecycle stay in the web app.

## Current State

- **In this repo**: `internal/commands/git_credential.go` implements the git
  credential helper (`driveHost()`: env `DRIVE_HOST`, default
  `drive.latere.ai`); `latere auth login` auto-wires it. The bearer is the
  refreshed auth.latere.ai identity token via `authIdentityToken`
  (lux.go:631). No `drive` command exists.
- **In ../drive**: the HTTP API under `/api/v1` is described by
  `docs/openapi.yaml` (generated, drift-proof). Drive validates auth-issued
  JWTs directly; authorization is claims-driven, no Drive-specific scopes.
  There is no Go client SDK; the CLI builds its own thin client.

## Architecture

- `internal/commands/drive.go` — the whole cobra tree, `newDriveCmd()`
  factory, registered via `root.AddCommand(newDriveCmd())` in root.go.
- `internal/drive/client.go` — thin typed client over Drive's `/api/v1`.
  Mirrors `internal/api/client.go` conventions (Bearer header,
  `User-Agent: latere-cli`, 60s timeout, non-2xx → typed error) but decodes
  Drive's error envelope.
- **Bearer**: auth identity token via `authIdentityToken` (auto-refresh),
  same path as git-credential and Lux. `--token` / `LATERE_DRIVE_TOKEN`
  passthrough for CI.
- **Base URL**: `resolveDriveURL(flag)` — flag `--drive-url` > env
  `DRIVE_API_URL` > default `https://drive.latere.ai`; copies
  `resolveLuxURL` (lux.go:596). `DRIVE_HOST` (git credential host matching)
  stays untouched.
- `drive` is added to `skipUpdateCheck` (root.go:66): `get -o -` streams file
  bytes to stdout and must stay clean.

## Command Space

Eight verbs, all under `latere drive`:

| Command | Does | API |
|---|---|---|
| `ls [prefix]` | List files under a prefix (default `files/`) | `GET /files/{owner}/{prefix}?list`; `--trashed` → `GET /trash` |
| `get <path>` | Download one file | `GET /files/…` (follow 302) |
| `put <src> [path]` | Upload one file (default path `files/<basename>`) | `PUT /files/…`, multipart >16 MiB |
| `mv <src> <dst>` | Move/rename within a space | `POST /files/…` `{move_to}` |
| `rm <path>` | Trash a file | `DELETE /files/…` |
| `restore <path>` | Undo: from trash, or to a prior version | `POST /trash/restore`; with `--version N` → `POST /files/…` `{restore_version}` |
| `history <path>` | List a file's versions | `GET /files/…?versions` |
| `share <path>` / `shares` / `unshare <id>` | Grant, list, revoke access | `POST /shares`, `GET /shares` (+`--inbox` → `/shared-with-me`), `DELETE /shares/{id}` |

Flags:

| Flag | On | Purpose |
|---|---|---|
| `--owner` | all (persistent) | Space: `me` (default), `org`, `u-<uuid>`, `o-<uuid>` |
| `--drive-url`, `--auth-url`, `--token` | all (persistent) | Endpoint/bearer overrides as above |
| `--json` | all (persistent) | Machine-readable output on stdout |
| `--long` | `ls` | Size, mtime, checksum columns |
| `--trashed` | `ls` | List trashed files instead of live ones |
| `--version N` | `get`, `rm`, `restore` | Operate on version N (download it / prune it / restore it) |
| `--permanent` | `rm` | Hard-delete instead of trash (also purges an already-trashed file) |
| `-o <file\|->` | `get` | Output destination (default: basename; `-` = stdout) |
| `--if-match <etag>`, `--create-only` | `put` | CAS overwrite / create-only (`If-None-Match: *`) |
| `--inbox` | `shares` | List shares granted *to* me instead of *by* me |

Paths are namespace-rooted exactly as in the API (`files/…`, `memory/…`,
`repos/…`, `workspaces/…`); no prefix-guessing. `put` upload strategy mirrors
the SPA: streaming PUT ≤16 MiB, multipart above (16 MiB parts, ≤1000 parts,
4 in flight, abort session on failure); `memory/**` requires CAS server-side
(428) and the CLI surfaces "pass --if-match or --create-only".
`share` flags (grantee / link, role, note) mirror the openapi `ShareCreate`
schema — confirm exact fields against `../drive/docs/openapi.yaml` at
implementation.

## Error Handling

- Not signed in: `authIdentityToken` failure surfaces
  `"not signed in; run latere login"` — same message family as review/lux.
- 403/404: pass the server message through with the request path for context
  (Drive hides admin resources as 404; agents get 403 on human-only writes).
- 409/412/428 on `put`: actionable CAS guidance.
- 413: distinguish quota exceeded vs the 100 MB single-PUT cap (multipart
  handles up to 16 GiB automatically, so 413 here means quota).
- Multipart failure: abort the session (`DELETE /uploads/{id}`) before
  returning.

## Testing Strategy

Follows `internal/commands` conventions (white-box, package `commands`):

- `TestDriveCommandRegisteredInRoot`; help-text tests via `executeForHelp`.
- `TestResolveDriveURL` — flag > env > default, copied from
  `TestResolveLuxURL` (lux_test.go:99).
- Flag-default tests (pattern: `TestReviewFlagDefaults`).
- `httptest.NewServer` Drive fakes; seeded tokens via `writeAuthTokenFile` +
  `isolateDriveTokens` (git_credential_test.go:22); cover 302-follow and the
  multipart flow.
- `internal/drive` client tests: error-envelope decoding, CAS headers,
  multipart part math.
- E2E (env-gated, like `cmd/latere/cella_import_e2e_test.go`): put → ls →
  get → history → rm → restore roundtrip against a real Drive.
- `TestSkipUpdateCheckForDrive`.

## Outcome

Shipped directly on main, same day. Drift: minimal — the eight-verb space
landed exactly as specced; one naming split (`shares --inbox` instead of a
flag on `share`) was already in the spec table. Commits: `19230df`
(internal/drive client, 87.8% coverage), `94e8e3f` (command tree + tests),
`3346fc1` (docs/drive.md + README row), `438824f` (live-API fix below),
`44a2fc0` (binary e2e).

**What shipped.** `internal/drive/client.go` — typed client over the wire
shapes extracted from `../drive/docs/openapi.yaml` (error envelope is a bare
`{"error"}`; versions use `version_no`/`superseded_at`; trash purge returns
a count). `internal/commands/drive.go` — the verbs, with
`TestDriveVerbSet` pinning the command space so growth requires a spec
change. Verified three ways: package tests, a binary-level e2e (roundtrip +
16 MiB multipart, byte-identical), and a live run against production Drive
(put/get/history/trash/restore/purge all exercised and cleaned up).

**Found against the live API.** Drive 400s a trailing slash on listing
paths (`files/` → "invalid path"); the client trims it (`438824f`). The
OpenAPI docs don't state this.

**Decisions.** Two security-relevant ones in the client: the bearer is
stripped on every redirect (Go only auto-strips cross-host; presigned URLs
reject double auth), and part PUTs go bare to the object store. `share`
grantee inference: `--to` containing `@` → email grant, otherwise principal
id; org/team/role grantee types stay web-only. `rm --permanent` falls back
to a trash purge when the live file is already gone, making it the single
"make it not exist" verb. Version pruning rides `rm --version` per the
flags table.

**Follow-ups.** The `shares` flags table mentions `--path-prefix` filtering
on `GET /shares` — not wired; add on demand. Multipart CAS rides the
complete call; a concurrent-writer e2e against staging would be nice but
needs a second identity. None blocking.

## Non-goals

- `drive whoami` — `latere whoami` owns identity.
- Workspace lifecycle (`/workspaces…`), attach/materialize/sync — the sandbox
  mount contract, driven by cella and the web app.
- Webhooks, events, stars, quota administration, admin plane, share-approval
  resolution — web-app flows; add individual verbs later only on demand.
- Git sugar (`clone`) and LFS — plain `git` works through the credential
  helper already.
- Public share-link download (`/api/v1/s/{token}/…`) — curl-able without auth.
