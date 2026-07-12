---
title: latere drive subcommand
status: drafted
depends_on: []
affects:
  - internal/commands/drive.go
  - internal/commands/drive_files.go
  - internal/commands/drive_shares.go
  - internal/commands/drive_ws.go
  - internal/commands/root.go
  - internal/drive/client.go
effort: large
created: 2026-07-12
updated: 2026-07-12
author: changkun
dispatched_task_id: null
---

# latere drive subcommand

## Overview

`latere drive` is the CLI face of Latere Drive (`../drive`, live at
`https://drive.latere.ai`). Today the CLI only wires Drive's git plane
(`latere git-credential`); the file plane — upload, download, list, trash,
versions, shares, workspaces, quota — is reachable only through the SPA or raw
curl. This spec defines the command space and options so the whole surface is
scriptable with the credentials `latere auth login` already retains.

## Current State

- **In this repo**: `internal/commands/git_credential.go` implements the git
  credential helper (`driveHost()`: env `DRIVE_HOST`, default
  `drive.latere.ai`) and `latere auth login` auto-wires it. The bearer it
  presents is the refreshed auth.latere.ai identity token via
  `authIdentityToken` (lux.go:631), falling back to the Cella token. No
  `drive` command exists.
- **In ../drive**: the full HTTP API under `/api/v1` is described by
  `docs/openapi.yaml` (generated, drift-proof; also served live at
  `GET /api/openapi.json`). Drive validates auth-issued JWTs directly — no
  Drive-specific scopes; authorization is claims-driven (`sub`,
  `principal_type`, `org_id`, `roles`, `is_superadmin`). There is no Go client
  SDK in the drive repo; the CLI builds its own thin client.

## Architecture

- `internal/commands/drive*.go` — cobra command tree, one file per group,
  following the `newXxxCmd()` factory pattern in root.go. Registered via
  `root.AddCommand(newDriveCmd())`.
- `internal/drive/client.go` — thin typed client over Drive's `/api/v1`.
  Mirrors `internal/api/client.go` conventions (Bearer header,
  `User-Agent: latere-cli`, 60s timeout, non-2xx → typed error), but decodes
  Drive's error envelope, not sandboxd's.
- **Bearer**: the auth identity token via `authIdentityToken` (auto-refresh),
  same path as git-credential and Lux — Drive accepts it directly. `--token` /
  `LATERE_DRIVE_TOKEN` passthrough for CI, mirroring `--token` on `latere lux`.
- **Base URL**: `resolveDriveURL(flag)` — flag `--drive-url` > env
  `DRIVE_API_URL` > default `https://drive.latere.ai`. Copies
  `resolveLuxURL` (lux.go:596) exactly. `DRIVE_HOST` (bare host, git
  credential matching) stays untouched; `drive clone` derives its remote URL
  from `resolveDriveURL`.
- `drive` is added to `skipUpdateCheck` (root.go:66): `get -` streams file
  bytes to stdout and must stay clean.

## Command Space

Persistent flags on `latere drive` (passed by pointer into child factories,
same as `newLuxCmd`):

| Flag | Default | Purpose |
|---|---|---|
| `--drive-url` | `""` | Drive base URL; empty = `DRIVE_API_URL` or `https://drive.latere.ai` |
| `--auth-url` | `""` | Auth base URL for token refresh; empty = `https://auth.latere.ai` |
| `--token` | `""` | Present this bearer instead of the stored identity token (env `LATERE_DRIVE_TOKEN`) |
| `--owner` | `"me"` | Space to operate in: `me`, `org`, `u-<uuid>`, `o-<uuid>` |
| `--json` | `false` | Machine-readable output on stdout |

Paths are namespace-rooted exactly as in the API (`files/…`, `memory/…`,
`repos/…`, `workspaces/…`); a bare `ls` defaults to the `files/` prefix. No
prefix-guessing magic — the CLI passes paths through verbatim.

### Files (phase 1) — `internal/commands/drive_files.go`

| Command | API | Flags |
|---|---|---|
| `drive whoami` | `GET /whoami` | — |
| `drive ls [prefix]` | `GET /files/{owner}/{prefix}?list` | `--long` |
| `drive stat <path>` | `HEAD /files/{owner}/{path}` | — |
| `drive get <path>` | `GET /files/{owner}/{path}` (follow 302) | `-o <file\|->` (default: basename), `--version N` |
| `drive put <src> <path>` | `PUT /files/…` or multipart via `POST /uploads` | `--if-match <etag>`, `--create-only` (`If-None-Match: *`), `--content-type` |
| `drive mv <src> <dst>` | `POST /files/…` `{move_to}` | — |
| `drive rm <path>` | `DELETE /files/…` | `--permanent`, `--version N` |
| `drive versions <path>` | `GET /files/…?versions` | — |
| `drive restore <path> --version N` | `POST /files/…` `{restore_version}` | `--version N` (required) |
| `drive trash ls` | `GET /trash?owner` | — |
| `drive trash restore <path>` | `POST /trash/restore` | — |
| `drive trash purge [path]` | `DELETE /trash` | `--all` when no path |
| `drive quota [owner]` | `GET /quotas/{owner}` | — |

`put` upload strategy: `src` of `-` streams stdin (single PUT only, requires
`--size` or buffers). Files ≤16 MiB use the streaming PUT; larger files use
the multipart session (`part_size` 16 MiB fixed, ≤1000 parts, 4 part-PUTs in
flight, abort session on failure) — same split the SPA uses. Single PUT is
capped by the server at 100 MB (`MAX_UPLOAD_SIZE`), so multipart is the
default above 16 MiB, not an option. `memory/**` writes require CAS
server-side (428); the CLI surfaces "pass --if-match or --create-only".

### Sharing & stars (phase 2) — `internal/commands/drive_shares.go`

| Command | API | Flags |
|---|---|---|
| `drive share create <path>` | `POST /shares` | flags mirror the openapi `ShareCreate` schema (grantee / link, role, note) — confirm exact fields against `docs/openapi.yaml` at implementation |
| `drive share ls` | `GET /shares` | — |
| `drive share revoke <id>` | `DELETE /shares/{id}` | — |
| `drive share inbox` | `GET /shared-with-me` | — |
| `drive star <path>` / `unstar <path>` | `PUT /stars` / `DELETE /stars` | — |
| `drive stars` | `GET /stars` | — |
| `drive events` | `GET /events?owner&after&limit` | `--after <cursor>`, `--limit` |
| `drive webhook ls\|create\|rm\|test` | `/webhooks…` | create: `--url`, `--actions`, `--path-prefix`; secret printed once |
| `drive quota set <owner> --limit <bytes>` | `PUT /quotas/{owner}` | org-admin only |

### Workspaces & git sugar (phase 3) — `internal/commands/drive_ws.go`

| Command | API | Flags |
|---|---|---|
| `drive ws ls` | `GET /workspaces` | `--deleted` |
| `drive ws create <slug>` | `POST /workspaces` | `--kind workspace\|repo` |
| `drive ws get <id>` | `GET /workspaces/{id}` | — |
| `drive ws rm <id>` / `ws restore <id>` | `DELETE` / `POST …/restore` | — |
| `drive clone <owner>/<slug> [dir]` | shells to `git clone {drive-url}/git/{owner}/{slug}.git` | relies on the credential helper already wired by login |

## Error Handling

- Not signed in: `authIdentityToken` failure surfaces
  `"not signed in; run latere auth login"` — same message family as review/lux.
- 401/403: Drive hides admin resources as 404 and returns 403 for agent
  principals on human-only mutations; pass the server message through with the
  request path for context.
- 409/412/428 on `put`: map to actionable CAS guidance (`--if-match`, file
  changed since read, `memory/` requires CAS).
- 413: distinguish quota exceeded vs single-PUT size cap (retry advice:
  multipart handles up to 16 GiB).
- 503 on `git-receive-pack` / locked workspace: report the writer lock.
- Multipart failure: abort the session (`DELETE /uploads/{id}`) before
  returning the error.

## Testing Strategy

Follows `internal/commands` conventions (white-box, package `commands`):

- `TestDriveCommandRegisteredInRoot`, help-text tests via `executeForHelp`.
- `TestResolveDriveURL` — flag > env > default, copied from
  `TestResolveLuxURL` (lux_test.go:99) with `t.Setenv`.
- Flag-default tests per subcommand (pattern: `TestReviewFlagDefaults`).
- `httptest.NewServer` fakes for the Drive API (list/get/put/multipart/trash),
  seeded tokens via `writeAuthTokenFile` + `isolateDriveTokens`
  (git_credential_test.go:22); 302-redirect and multipart part-PUT flows
  covered against the fake.
- `internal/drive` client unit tests: error-envelope decoding, CAS headers,
  multipart part math (16 MiB parts, 1000-part cap).
- E2E (gated by env, like `cmd/latere/cella_import_e2e_test.go`): put → ls →
  stat → get → versions → rm → trash restore roundtrip against a real Drive.
- `TestSkipUpdateCheckForDrive` mirroring the git-credential one.

## Non-goals

- Admin plane (`/api/v1/admin/*`) and org share-approval resolution — web
  flows, out of CLI scope for now.
- Workspace attach/materialize/sync — that is the sandbox mount contract
  (`../drive/docs/mount-contract.md`), driven by cella, not by users.
- Git LFS plumbing — `git lfs` speaks to Drive directly through the credential
  helper.
- Public share-link download (`/api/v1/s/{token}/…`) — curl-able without auth;
  add later if asked.
