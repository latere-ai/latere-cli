---
title: "`latere apps`: a new app is private, and `visibility`, `access`, `share` and `unshare` decide who can open it"
status: implemented
depends_on:
  - specs/009-repos-command.md
  - specs/010-app-command.md
  - specs/011-capability-commands.md
  - "CROSS-REPO: insula api/openapi.yaml, POST /v1/apps with no visibility and PATCH /v1/apps/{slug} with one"
  - "CROSS-REPO: insula specs/038-the-gallery-listing-and-reports.md, only a listing in the gallery makes an app public, and a change of visibility withdraws the listing"
  - "CROSS-REPO: platform specs/174-sharing-an-app.md and internal/web/apps_access.go, the people with access under /v1/account/apps/{app_id}/access"
affects:
  - internal/commands/app.go (create names no visibility; list and show say who can open an app; the group's help)
  - internal/commands/app_access.go (new: visibility, access, share, unshare)
  - internal/commands/repos.go (a refusal in the shared envelope keeps its details)
  - cmd/latere/app_access_e2e_test.go (new), cmd/latere/app_e2e_test.go
  - docs/apps.md, docs/configuration.md, docs/login-and-tokens.md, README.md, CHANGELOG.md
created: 2026-10-11
updated: 2026-10-11
author: changkun
---

# Who can open an app, from the CLI

Every app `latere apps create` made was open to anyone: the command sent
`visibility: public` on each create (010), and the CLI had no command to
change who can open an app or to share it. The owner decided that a new app
is private until its owner opens it, and that the owner opens it to anyone
who has its address or shares it with people by email, from the chat, the
console and the CLI. This records the CLI's part.

## Current state

| Fact | Where |
|---|---|
| `POST /v1/apps` takes an optional `visibility`; without one the platform chooses: `private` in an organization, and in a personal account `public` until the platform release that makes it `private` there too | insula `api/openapi.yaml`, `createApp`; the owner's decision of 2026-10-11 |
| `PATCH /v1/apps/{slug}` with `{"visibility"}` answers 200 with the app, decided as `app.settings`; a caller who may not is 403 `forbidden` | insula `api/openapi.yaml`, `updateApp` |
| Visibility has three values. `private`: the owner, an organization's members and the people invited, after signing in at the gate. `unlisted`: anyone with the address, kept out of search engines. `public`: anyone, and search engines may list it | insula `api/openapi.yaml`, `Visibility` |
| An app becomes `public` when its listing in the gallery is approved; a change to `unlisted` or `private` deletes the listing, shown or waiting | insula spec 038 |
| The people invited to an app are platformd's: `GET /v1/account/apps/{app_id}/access`, `POST .../access/invites`, `DELETE .../access/{access_id}`, keyed by the app's id, not its slug | platform `internal/web/apps_access.go` |
| Those routes take a bearer for the audience `api.latere.ai`, and are served at `https://platform.latere.ai` as well as at the origin | measured 2026-10-11: the list of an app read with the token `latere repos` mints |
| An invite is not mailed; the invited person signs in with an account whose verified email is the invited address | platform spec 174 |

## Design

### The two values a person chooses

A person chooses `private` or `unlisted`. The CLI never sends `public`: only
an approved listing in the gallery makes an app public, and a visibility the
CLI could set would skip that review. `public` given to `--visibility` or to
`visibility` is refused before any request with one sentence that says how an
app becomes public. Any other value is refused with the two choices.

An app that is `public` already is read as it is: `list` prints the value,
and `show`, `access`, `create` and `visibility` say that anyone can open it
and that search engines may list it.

`create` sends no `visibility` unless `--visibility` is given, so the default
is the platform's alone and is written nowhere in the CLI. Every command
prints the visibility its answer carries.

### Commands

| Command | Routes | Behavior |
|---|---|---|
| `create [name] [--visibility value]` | `POST /apps` with `{name?, slug?, visibility?}` | adds `visibility` and one sentence on who can open the app to what 010 prints, and for a private app the two commands that open it or share it |
| `visibility <value> [slug] [--json]` | `GET /apps/{slug}`, then `PATCH /apps/{slug}` with `{visibility}` | prints the app, its address, the new visibility and who can open it; says so when nothing changed, when a public app left the gallery, and when an unlisted app made private ended a waiting listing |
| `access [slug] [--json]` | `GET /apps/{slug}`, then `GET /v1/account/apps/{id}/access` at the platform | the visibility in words, then the people invited: address, state, name once accepted, age of the invitation |
| `share [slug] <email>... [--json]` | `GET /apps/{slug}`, then `POST .../access/invites` with `{emails, role: viewer}` | who was invited, who was skipped and why, that invitations are not mailed, and the address to send |
| `unshare [slug] <email>...` | `GET /apps/{slug}`, `GET .../access`, then one `DELETE .../access/{access_id}` for each address listed | who is no longer invited, and which addresses were not invited |
| `list` | as 010 | a `VISIBILITY` column |
| `show [slug]` | as 010 | the visibility followed by who it lets open the app |

`visibility` reads the app before it changes it because the answer of the
change carries the new value alone, and what the change ended depends on the
one before.

`share` and `unshare` take the slug first when it is given. A slug never
holds an `@`, so an argument without one in first position is the slug, and
an argument without one anywhere else is refused before any request. The
slug left out is read from the git remote, as in 010. The hint for a missing
slug is the command's own usage with `[slug]` made required, since the slug
is not always the first argument.

`unshare` matches an address to its invitation without case, as the platform
keeps addresses. An address that is not invited is reported and exits 0: the
platform answers the removal of nothing the same way. A removal that fails
stops the command with exit 1 after it has printed what it took back.

`--json` prints the API's own bytes: the App for `visibility`, the invite's
answer for `share`, and for `access` an object with `app` and `access`, each
as its API answers it.

### Two APIs, two tokens

The visibility is the Apps API's, reached as in 010 with a token for
`insula`. The people invited are platformd's, reached through the call
`latere repos` already makes (009): the address `--platform-url` or
`LATERE_PLATFORM_URL` names, `https://platform.latere.ai` otherwise, and a
token minted for `api.latere.ai`, or `LATERE_PLATFORM_TOKEN` as given. The
sharing commands add `--platform-url`; no second mint and no second address
is written.

### Limits and the role

The platform bounds the people one app has invited, the addresses of one
invite, and the invites a person makes in a day. The CLI holds none of the
three numbers: it sends what it was given and explains the refusal from the
refusal's own details. Every invitation is made as `viewer`, the role that
opens the app and nothing else of it, stated in the request rather than left
to the platform's default so that a later default cannot widen it.

### Refusals

| Refusal | What the command says |
|---|---|
| 403 `forbidden` on a change of visibility | that the person may not change who can open the app, the authorizer's reason, and who can |
| 400 `invalid_field` with `details.index` on an invite | the address that is not one, and that no one was invited |
| 429 `invite_limit` | which bound was met with the number the refusal carries, when the person can invite again for the daily one, and that no one was invited |
| 404 `access_not_found` | that the platform found no such app in the current context |
| 401 | the line that says to run `latere login` |

A refusal in the platform's shared envelope kept its code and sentence
only. It now keeps its `details`, and prints `details.detail` as its detail
line, which every platform command gains.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| The values offered | `private` and `unlisted` | all three: `public` from the CLI would make an app listable by search engines with no review, which the gallery's listing exists to prevent |
| The default of a create | no `visibility` sent | sending `private`: the CLI would restate a default the platform owns, and would be wrong again the next time it changes |
| Where the people invited are reached | `platformCall` at the platform's address with the platform's audience | a second client at the origin: one more copy of the mint and the address resolution |
| Bounds on an invite | none in the CLI | reading `limits` first and splitting an invite: a partial success to explain, for a bound few invites meet |
| An address not invited, on `unshare` | reported, exit 0 | exit 1: the state the person asked for already holds |
| `visibility` in `list` | a column | left to `show`: with apps starting private, which apps are open is what a list is read for |

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | `create` sends no `visibility` unless `--visibility` names one, and prints the visibility of the answer with who can open the app | `TestAppCreateOutsideARepository`, `TestAppCreateWithoutANameSendsAnEmptyBody`, `TestAppCreateSendsTheVisibilityNamed`, `TestAppCreatePrintsTheVisibilityOfTheAnswer`, `TestAppE2E` |
| 2 | `public` and any value that is neither choice are refused before any request, by `create` and by `visibility` | `TestAppCreateRefusesAVisibilityAPersonCannotChoose`, `TestAppVisibilityRefusesWhatAPersonCannotChoose`, `TestAppAccessE2E` |
| 3 | `visibility` sends the value alone, prints who can open the app, and says what the change ended | `TestAppVisibilityChangesWhoCanOpenTheApp`, `TestAppVisibilityReadsTheSlugFromTheRemote`, `TestAppVisibilityJSONIsTheAPIsApp`, `TestAppAccessE2E` |
| 4 | A refused change of visibility names what the person may not do | `TestAppVisibilityRefusals`, `TestAppVisibilityOfAnUnknownApp` |
| 5 | `access` prints the visibility in words and the people invited, for each visibility and for an organization's app | `TestAppAccessListsThePeopleInvited`, `TestAppAccessByVisibility`, `TestAppAccessJSONIsTheAppAndItsAccessList` |
| 6 | `share` invites as `viewer`, reports who was skipped and why, says invitations are not mailed with the address to send, and says what an invitation is for on an app anyone can open | `TestAppShareInvitesByEmail`, `TestAppShareReadsTheSlugFromTheRemote`, `TestAppShareSaysWhoWasSkippedAndWhy`, `TestAppShareOfAnAppAnyoneCanOpen`, `TestAppShareJSONIsThePlatformsAnswer` |
| 7 | `unshare` removes by address, reports an address not invited without failing, and stops at a removal that fails | `TestAppUnshareTakesBackByAddress`, `TestAppUnshareOfAnAddressNotInvited`, `TestAppUnshareStopsAtAFailedRemoval`, `TestAppUnshareRefusedTheList` |
| 8 | An argument that cannot be an address is refused before any request, and a missing slug names the command with the slug in its place | `TestAppShareAndUnshareArguments`, `TestAppWithoutASlugOrARemote` |
| 9 | The Apps API receives only tokens for `insula` and the platform only tokens for `api.latere.ai`, at the address and with the bearer `latere repos` takes | `TestAppAccessListsThePeopleInvited`, `TestAppAccessPlatformAddressAndBearer`, `TestAppAccessE2E` |
| 10 | Every refusal of the platform's access routes reads as a sentence naming what its details carry | `TestAppAccessRefusals`, `TestAppShareRefusals`, `TestPlatformErrorKeepsTheSharedEnvelopesDetails` |
| 11 | `list` and `show` read `private`, `unlisted` and `public` correctly, and the help offers two choices | `TestAppListShowsWhatProductionServes`, `TestAppShowSaysWhoCanOpenTheApp`, `TestAppWithoutASubcommandShowsHelp` |

## Outcome

Built on 2026-10-11, released in v0.18.0. Every criterion has a passing test, and
`access` was read against the platform with the built binary.

The first design offered all three values. The owner's decision that only
the gallery makes an app public arrived during the build, and the two-value
form above is what was built.

One command makes one mint for the Apps API and one for each request to the
platform, so `unshare` of several addresses mints once for each. `platformCall`
mints for each call, which the one-request repository commands never
noticed; holding the bearer for a command is a change to that call, not to
these commands.
