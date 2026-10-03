---
title: "`latere app`: create apps and follow their builds on the Apps API"
status: implemented
depends_on:
  - specs/001-auth-unification-migration.md
  - "CROSS-REPO: insula api/openapi.yaml at v0.1.3, the Apps API the platform origin serves at /v1/apps"
  - "CROSS-REPO: insula specs/014-cli.md, the command group's full design, of which this is the part the API serves today"
  - "CROSS-REPO: auth deploy/base/clients.yaml, the latere-cli row's actor_audiences, which lists insula"
affects:
  - internal/commands/app.go (new)
  - internal/commands/app_logs.go (new)
  - internal/commands/root.go (the command group)
  - cmd/latere/app_e2e_test.go (the built binary against a stub of the API and the mint)
  - .lateregate.yaml (the identity block's audience)
  - docs/app.md, docs/configuration.md, docs/login-and-tokens.md, README.md, CHANGELOG.md
created: 2026-10-03
updated: 2026-10-03
author: changkun
---

# `latere app`

A person who wants an app on the platform had to open the console to create
it and to see its builds. Deploying is a git push to the app's repository,
which `latere login` already lets git authenticate, so the CLI needs no
deploy command: it needs to create the app, point the repository at it, and
show what the push built.

Insula's own design for this group, its spec 014, is larger than what the API
serves today (no upload deploys, no releases by API, no environment). This
records the part built on the API as it is at v0.1.3, under the names that
design uses.

## Design

### Address and credential

| Setting | Value |
|---|---|
| Base URL | `https://api.latere.ai/v1/apps`, the API under the platform origin, whose `/v1` root becomes `/v1/apps`; `LATERE_APP_URL` or `--api-url` overrides it |
| Bearer | an actor token minted at auth for the audience `insula`, one per command; `LATERE_APP_TOKEN` presents a bearer as given |
| Issuer | inferred from the base URL's host, or `AUTH_URL`, or `--auth-url` |

The API accepts `insula` and `api.latere.ai`. The CLI mints `insula`, the
name the latere-cli client's row lists for this API, so a token for the
Apps API is not also a token for platformd's own routes.

### Commands

| Command | Routes | Behavior |
|---|---|---|
| `create [name] [--slug] [--remote] [--no-remote] [--json]` | `POST /apps` with `{name?, slug?, visibility: public}` | prints the address, the newest-preview address and the push URL; in a git repository adds the push URL as the remote `latere`; ends with the next command, `git push latere main` |
| `list [--json]` | `GET /apps` every page, `GET /apps/{slug}/releases` per app | slug, state, the tag of the newest `released` release or `-`, address |
| `show [slug] [--json]` | `GET /apps/{slug}`, `/releases`, `/deploys` | addresses, the live release and its deploy, the newest preview deploy, push and clone URLs |
| `deploys [slug] [--json]` | `GET /apps/{slug}/deploys` | short id, ref without `refs/heads/` or `refs/tags/`, status, the deploy's preview address, age |
| `logs [slug] [deploy] [-f] [--json]` | `GET /apps/{slug}/deploys` then `GET /apps/{slug}/deploys/{id}/logs` | the stored log as NDJSON, or with `-f` the event stream until its `end` event |
| `delete <slug> [--yes]` | `DELETE /apps/{slug}` | asks for the slug typed back; says the slug is held for seven days |

`[slug]` left out is read from the git remote `latere`: the last path segment
of its URL without `.git`. With no such remote the command names the missing
argument and makes no request. An `app_not_found` for a slug read this way
says where the slug came from.

`--json` prints the API's own bytes: the App for `create`, an array of Apps
for `list`, an array of Deploys for `deploys`, and for `show` an object with
`app`, `release` (the live Release, or null) and `preview` (the newest preview
Deploy, or null). `logs --json` prints each LogLine as one line.

### The remote

`create` adds the remote only in a git repository and only when no remote of
that name exists. A remote of that name that names another app refuses the
create before the request, with exit 1, so no app is created that the
repository does not push to. A remote naming the slug `--slug` asks for is a
deleted app's, which the create restores; after the create, a remote equal to
the push URL is left as it is, and a different one is reported and left as
it is.

### Following a build

The event stream's `data:` frames are LogLines, `:` lines are heartbeats, and
the last frame is `event: end` with a LogEnd. The command exits 0 on the
status `built`, and also on `ready` and `live`, the deploy statuses past it.
It exits 1 on `failed`, printing `deploy <id> failed in <component>: <code>:
<message>` and the hint, and on `canceled`. A stream that closes without an
`end` exits 1.

A push returns before the deploy it creates is recorded, so the newest deploy
right after `git push` is the previous one. When the slug comes from the
remote and no `[deploy]` is given, `logs -f` follows the newest deploy whose
`commit_sha` is the commit `HEAD` names, reading the deploys every two seconds
for up to a minute until it appears. Every other form takes the newest.

The deploy of a release has `from_deploy`, the id of the preview whose build
it serves, and has no build log of its own. For it, `logs` prints `Deploy
<id> released <tag> from preview <id> without a build; its build log:` to
stderr and reads the preview's log instead, stored or followed, and with
`--json`. With `--follow`, a deploy that is `waiting` or `queued` prints
`Waiting for the build to start...` before its first line.

Log lines are printed as `<time> <src> <line>`, with the component before the
step when the deploy builds more than one. Escape sequences in a line are
kept when stdout is a terminal and removed otherwise.

### Refusals

The API answers `{"error": {"code", "message", "details"}}` with `details.hint`
on every refusal. The CLI prints `<code>: <sentence>` and the hint, and exits 1.
For `create`, the sentence says what the details name: the slug for
`slug_taken`, `slug_reserved` and `slug_held`, the hold's end for `slug_held`,
the rule for `invalid_slug` (`short` reads "a slug under three characters
needs a plan that allows short slugs"), the attempts for
`slug_generation_failed`, the stage for `repository_create_failed`, the
authorizer's reason for `forbidden`. A `401` other than `audience_mismatch`
adds the line that says to run `latere login`.

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | `create` sends `visibility: public` with the name and slug given, and prints the addresses, the push URL and the next command | `TestAppCreateOutsideARepository`, `TestAppCreateWithoutANameSendsVisibilityAlone`, `TestAppE2E` |
| 2 | `create` adds the remote, under `--remote` when given, skips it with `--no-remote`, refuses a remote of another app before any request, and keeps a remote it would overwrite | `TestAppCreateAddsTheRemote`, `TestAppCreateNoRemoteLeavesTheRepositoryAlone`, `TestAppCreateRefusesARemoteOfAnotherApp`, `TestAppCreateRestoreKeepsTheSameRemote`, `TestAppCreateKeepsADifferentRemoteOfTheSameSlug` |
| 3 | Every refusal the API lists for a create reads as a sentence naming what its details carry | `TestAppCreateRefusals`, `TestAppAudienceMismatchKeepsTheAPIsHint` |
| 4 | `list`, `show` and `deploys` print what production serves, the newest preview and the deploys, and `--json` prints the API's resources | `TestAppListShowsWhatProductionServes`, `TestAppListJSONIsTheAPIsApps`, `TestAppShow`, `TestAppShowJSON`, `TestAppDeploys` |
| 5 | A slug left out is read from the remote `latere`, and its absence names the missing argument with no request | `TestAppSlugFromTheRemote`, `TestAppWithoutASlugOrARemote`, `TestAppSlugFromAStaleRemoteSaysWhereItCameFrom` |
| 6 | `logs -f` passes heartbeats, exits 0 on `built`, `ready` and `live`, and exits 1 with the code, message and hint on `failed`, on `canceled`, and on a stream that closes early | `TestAppLogsFollowEndsWithTheBuild`, `TestAppLogsFollowOfAFailedBuild`, `TestAppLogsFollowOfACanceledBuild`, `TestAppLogsFollowOfAStreamThatClosesEarly`, `TestAppE2E` |
| 7 | `logs -f` after a push follows the deploy of `HEAD`, waiting for it, and gives up after the wait | `TestAppLogsFollowWaitsForTheDeployOfHEAD`, `TestAppLogsFollowGivesUpOnTheDeployOfHEAD`, `TestAppLogsWaitsOnlyWhenFollowingThePushedCommit` |
| 8 | Escape sequences are removed off a terminal and kept on one | `TestAppLogsStoredOfTheNewestDeploy`, `TestAppLogsKeepsColorOnATerminal`, `TestStripANSI` |
| 9 | `delete` asks for the slug typed back unless `--yes`, and makes no request on a mismatch | `TestAppDelete` |
| 10 | The bearer is an actor token for `insula`, never the login token, unless `LATERE_APP_TOKEN` is set | `TestProductCredentialsCarryOnlyTheirOwnAudience`, `TestAppTokenFromTheEnvironment`, `TestAppE2E` |
| 11 | The log of a release's deploy is the log of the preview it released, said on one line, stored, followed and in JSON; a queued deploy followed says it waits for the build | `TestAppLogsOfAReleaseIsItsPreviews`, `TestAppLogsFollowOfAQueuedDeploy` |

## Outcome

Built on 2026-10-03, not released. Every criterion has a passing test.
