---
title: "`latere apps name`: change the name people read for an app and keep its address"
status: implemented
depends_on:
  - specs/010-app-command.md
  - specs/014-app-rename.md
  - "CROSS-REPO: platform specs/175-app-names-and-pins.md: Rename changes the name, never the address"
  - "CROSS-REPO: platform docs/apps/openapi.yaml, updateApp: `name`, the display name, which may not be blank"
affects:
  - internal/commands/app_name.go (new: name)
  - internal/commands/app_rename.go (--name, hidden, names the command that changes the name)
  - internal/commands/app.go (the command registered, the group's help)
  - internal/commands/app_name_test.go, cmd/latere/app_name_e2e_test.go (new)
  - docs/apps.md, README.md, CHANGELOG.md
created: 2026-10-29
updated: 2026-10-29
author: changkun
---

# An app's name, changed from the CLI

An app has a name, the words people read for it, apart from its slug, its
address. `latere apps create [name]` sets the name, and nothing in the CLI
changed it afterwards: `latere apps rename` changes the slug alone (014).
The owner's review app `latere-clients` kept the name `latere-studio` it was
created with for that reason.

## Current state

| Fact | Where |
|---|---|
| `PATCH /v1/apps/{slug}` with `{"name": "…"}` changes the name alone; a blank name is `invalid_request` with `details.field` `name` | platform `docs/apps/openapi.yaml`, `updateApp` and `AppUpdate` |
| The other clients' Rename changes the name and never the address; changing the address is a separate step | platform spec 175 |
| The SDK's `apps.UpdateAppParams.Name` is the display name | sdk-go `apps/apps.go` |

## Design

| Command | Route | Behavior |
|---|---|---|
| `name <name> [slug]` with `--json` | `PATCH /apps/{slug}` with `{name}` | trims the name, refuses a blank one before anything is sent, changes it, prints the app's slug, name and address, and says the address and push URL stay |

`[slug]` is resolved as in 010. Nothing is read first: a name change
moves nothing a later step depends on, as a rename's push URL does.

`rename` keeps its meaning. It takes a hidden `--name`, the flag a person
reaches for after using the other clients' Rename, which sends nothing and
prints the `latere apps name` line that does it, with the slug when one was
given.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| The command's shape | its own verb, `name <name> [slug]`, as `visibility <visibility> [slug]` | `rename --name <name> [slug]`: with one argument it would be the new slug without `--name` and the app's slug with it, and a wrong guess changes another app |
| `rename --name` | refused, with the command that changes the name | accepted as `name`: the same ambiguity |

## Acceptance criteria

| # | Criterion | Test |
|---|---|---|
| 1 | A change sends the trimmed name alone and prints the slug, the name and the address | `TestAppNameChangesTheNameAlone` |
| 2 | The slug is read from the remote; `--json` prints the app | `TestAppNameReadsTheSlugFromTheRemote` |
| 3 | A blank name sends nothing; `forbidden`, `invalid_request` and `app_deleting` say what they were about | `TestAppNameRefusals` |
| 4 | `rename --name` sends nothing and names the command | `TestAppRenameWithANamePointsAtName` |
| 5 | The built binary changes the name against a stub with auth's mint | `cmd/latere.TestAppNameE2E` |

## Outcome

Built as designed.
