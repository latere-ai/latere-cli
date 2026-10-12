---
title: "`latere apps rename`: an app moves to a new slug, the command says what became of the old address, and sets the git remote that held the old push URL"
status: implemented
depends_on:
  - specs/010-app-command.md
  - specs/012-app-access.md
  - "CROSS-REPO: insula specs/006-projects-and-slugs.md, section Rename and the error table: PATCH /v1/apps/{slug} with a new slug, the hold on the old one, and no redirect"
  - "CROSS-REPO: insula specs/026-project-repository.md: a rename renames the app's repository on the git host first, so the push URL changes, and the refusals of that step"
  - "CROSS-REPO: platform specs/194-an-app-changes-its-address.md: Change address in the console, the same change in the words of a screen"
affects:
  - internal/commands/app_rename.go (new: rename)
  - internal/commands/app.go (the command registered, the group's help, one constant for a slug's length and one for the hold, the slug refusals shared by create and rename)
  - cmd/latere/app_rename_e2e_test.go (new)
  - docs/apps.md, README.md, CHANGELOG.md
created: 2026-10-12
updated: 2026-10-12
author: changkun
---

# An app's address, changed from the CLI

An app answers at `https://<slug>.latere.site`. `latere apps create` names
the slug or lets the platform pick one, such as `calm-otter-4821`, and until
now nothing in the CLI changed it afterwards. The owner asked on 2026-10-12:
"there is no way to change the slug of an app? ... users should want to
update the url to make the url more meaningful anyway." The Apps API has
renamed an app since its first build, and its own spec names the command
that was never written. This records it.

## Current state

| Fact | Where |
|---|---|
| `PATCH /v1/apps/{slug}` with `{"slug": "<new>"}` moves the app: its address and preview addresses carry the new slug, and the answer is the app as every other answer of it | insula spec 006, Rename |
| The app's repository is renamed on the git host before anything is written, so `repository.push_url` changes and `repository.clone_url`, which is by id, does not | insula spec 026 |
| The old address answers 404 at once and is not redirected, by decision | insula spec 006 |
| The old slug is held for the account that had it for seven days. The same account takes it again at once; nobody else can until the hold ends | insula spec 006, `internal/store/model.go` |
| A slug is 1 to 40 characters of `[a-z0-9-]`, starts and ends with `[a-z0-9]`, and holds no `--`; the rules are checked in that order, in bytes. A slug under three characters needs a plan that allows it | insula `internal/slug/slug.go` |
| A new slug that is the app's own is no rename: the answer is 200 and nothing is held | insula `internal/api/apps.go` |
| Refusals: `invalid_slug` 400 with `details.reason`, `slug_reserved` 409, `slug_taken` 409, `repository_name_taken` 409, `repository_unavailable` 503, `forbidden` 403, `app_not_found` 404, `app_deleting` 409. Each leaves the app as it was | insula spec 006, platform `docs/apps/openapi.yaml` |
| A rename is told that a slug held for another account is `slug_taken`, with no end of the hold. `slug_held` at a rename is the git host's refusal of a name it still keeps for a repository deleted recently, with `details.reason` `repository_name_held`. Spec 006's table lists `slug_held` with `hold_ends_at` for the code as a whole, which a create earns | insula `internal/api/apps.go`, `internal/gitplane/gitplane.go` |
| `apps create` adds the push URL as the git remote `latere`, and every command that takes `[slug]` reads the slug from that remote | 010 |

## Design

### Command

| Command | Routes | Behavior |
|---|---|---|
| `rename <new-slug> [slug]` with `--json` | `GET /apps/{slug}`, then `PATCH /apps/{slug}` with `{slug}` | checks the new slug, reads the app, renames it, prints the app at its new address, what became of the old one and of the hold, and sets the git remote `latere` when it held the old push URL |

`[slug]` is resolved as in 010: the argument, else the slug the `latere`
remote of the current repository names.

### The order of a rename

1. The new slug is held to the four rules the platform checks first, in its
   order: length, characters, the hyphen at either end, two hyphens in a
   row. One that breaks a rule ends the command before the slug of the app
   is resolved, with the rule's sentence and "nothing was sent".
2. The app's slug is resolved, and the `latere` remote of the current
   repository is read.
3. The app is read. Its answer names the old push URL, which the answer of
   the rename no longer does.
4. A new slug that is the app's own ends the command with the app as it is
   and the line that nothing changed, exit 0, with no `PATCH`.
5. The rename is sent with the slug alone.
6. The remote is set when it is the old push URL.

Everything that can stop the command without the platform's write comes
before step 5, so a refusal there leaves the app and the repository as they
were.

A slug under three characters is not checked in step 1: a plan may allow
one, so the platform's `invalid_slug` with the reason `short` is the answer.

### What it prints

The app's slug, address, preview address and push URL, as `create` prints
them. Then two sentences: that the old address stopped working, that the
previews moved, and that nothing sends a visitor on from the old address;
and that the old slug stays held for seven days, for your account or for
the organization that owns the app, with the command that renames back.

Then one line for the remote:

| The current directory | What the command does | What it says |
|---|---|---|
| a repository whose remote `latere` is the old push URL | `git remote set-url latere <new push URL>` | that it set the remote |
| a repository whose remote `latere` is already the new push URL | nothing | that it already points there |
| a repository whose remote `latere` is anything else | nothing | where the remote points, that it was left, and the `git remote set-url` line |
| a repository with no remote `latere` | nothing | that none was changed, and the line |
| no repository | nothing | that none was changed, and the line |

The remote is matched to the old push URL exactly, as `create` matches it.
A remote of the same slug under another owner, or the same repository
written another way, is not this command's to rewrite.

A `git remote set-url` that fails after the rename ends the command with
exit 1: the app is renamed, and the error says so with the line to run, as
`create` says a remote it could not add.

`--json` prints the app as the API answers it, and the line for the remote
goes to standard error.

### Refusals

Each has its own sentence under its code, and every one exits 1 with nothing
on standard output.

| Code | The sentence says |
|---|---|
| `slug_taken` | another app has the slug, or it is held for the account that gave it up |
| `slug_reserved` | the platform keeps the slug |
| `slug_held` with `repository_name_held` | the slug is the name of a repository deleted recently, still held |
| `slug_held` otherwise | the slug is held for its previous owner, until the moment `hold_ends_at` names when it names one |
| `invalid_slug` | the rule `details.reason` names |
| `repository_name_taken` | the app's owner already has a repository of that name, which the app's repository would take |
| `repository_unavailable` | the repository could not be renamed, nothing was changed, run again in a few minutes |
| `forbidden` | you may not rename the app, with the reason when one is named, and who can |
| `app_not_found` | the platform's sentence, with where the slug came from when it was read from the remote |

The sentences of `slug_reserved`, `slug_held` with its end and `invalid_slug`
are the ones `create` prints, from one function.

### One place for each limit

`appSlugMax` is the 40 characters and `appSlugHold` the seven days. The
rule's sentence, the help of `create`, `rename` and `delete`, the check
before a rename and what `delete` and `rename` print read them from there.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| Whether to ask first | no question, as `visibility` | typing the slug back with `--yes`, as `delete`: a rename is undone by renaming back within the hold, and a delete is not. The help and the output both say that links to the old address stop working |
| Checking the slug before sending | the four rules of its form, stated once with their sentences | sending and letting the platform refuse: a typing mistake would cost a request and read as the platform's refusal. Checking a short slug too: a plan may allow one |
| The app read before the rename | one `GET` | deriving the old push URL from the new one by swapping the last segment: a guess at the git host's layout, which the answer of the read states |
| A new slug that is the app's own | no `PATCH`, exit 0, says nothing changed | sending it: the platform changes nothing, and the output would read as a rename. Exit 1: the state asked for already holds |
| Which remote is set | `latere`, when its URL is the old push URL exactly | every remote that holds the old push URL: `create --remote` may have named another, which the line the command prints covers. A looser match: it would rewrite a remote of another owner's repository that shares the slug |
| A remote that points elsewhere | left, with the line to run, exit 0 | exit 1: the rename asked for was made |
| A remote that could not be set | exit 1 with the line to run, as `create` | exit 0: the command said it would set the remote and did not |
| `slug_taken` at a rename | says both things it can mean | the sentence of `create`, "already in use": at a rename the platform also answers it for a slug held for another account |
| `--json` | the app as the API answers it, notes on standard error | an object of the command's own with the old and the new: `create` and `visibility` print the API's own app |
| A flag for the old address | none | `--redirect`: the platform does not redirect, by decision |

## Not in this spec

Changing an app's display name. Setting a remote that `create --remote`
added under another name, or one written as an SSH address. A redirect from
the old address.

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | A rename reads the app, sends the new slug alone, and prints the new address, preview address and push URL, that the old address stopped with no redirect, and the hold with the command that renames back | `TestAppRenameMovesTheApp`, `TestAppRenameE2E` |
| 2 | In a repository whose remote `latere` is the old push URL the remote is set to the new one, whether the slug was given or read from the remote, and no other remote is touched | `TestAppRenameSetsTheRemote`, `TestAppRenameE2E` |
| 3 | A remote of another owner, of another app, or of the same repository written another way is left, and so is a repository with no remote `latere`; each prints the `git remote set-url` line, and a remote already at the new push URL says so | `TestAppRenameLeavesARemoteThatPointsElsewhere` |
| 4 | Each rule of a slug's form is told from the others in the platform's order, with one sentence each from the one length constant | `TestBrokenSlugRule` |
| 5 | A slug that breaks a rule sends nothing, mints nothing, prints nothing, leaves the remote and exits 1; a short slug is sent and the platform's refusal is said | `TestAppRenameSendsNothingForASlugThatBreaksARule`, `TestAppRenameLeavesAShortSlugToThePlatform`, `TestAppRenameE2E` |
| 6 | Each refusal has its own sentence under its code, exits 1, prints nothing on standard output and leaves the remote | `TestAppRenameRefusals`, `TestAppRenameE2E` |
| 7 | An app the account does not have is said before any change is asked for, with where an inferred slug came from | `TestAppRenameOfAnUnknownApp`, `TestAppWithoutASlugOrARemote` |
| 8 | A new slug that is the app's own sends no change and says so, exit 0 | `TestAppRenameToTheSlugTheAppHas`, `TestAppRenameE2E` |
| 9 | An organization's app names the organization as the one the slug is held for | `TestAppRenameOfAnOrganizationsApp` |
| 10 | `--json` prints the API's own app, and what became of the remote goes to standard error | `TestAppRenameJSONIsTheAPIsApp`, `TestAppRenameE2E` |
| 11 | A remote git cannot write ends the command with exit 1, the app renamed, and the line to run | `TestAppRenameSaysARemoteItCouldNotSet` |
| 12 | The help of the command and of the group say what a rename does, with the length and the hold from their constants; the arguments are one or two | `TestAppRenameHelp`, `TestAppRenameArguments` |
| 13 | `create` and `delete` say the same sentences as before from the shared constants and the shared slug refusals | `TestAppCreateRefusals`, `TestAppDelete` |

## Outcome

Built on 2026-10-12, not released. Every criterion has a passing test.

Insula's spec 006 lists `slug_held` with `hold_ends_at` among the refusals a
slug earns, and a rename was expected to answer it for a slug held for
another account. The service as built answers that `slug_taken` at a rename,
and keeps `slug_held` there for the git host's hold on a deleted
repository's name. The command says each as the service means it, and still
reads `hold_ends_at` when an answer carries one.

`create`'s refusal sentences moved into a function `rename` shares, and its
help now builds the slug's length and the hold from their constants. Its
words are unchanged except for one added line that names `rename`.

The stub of the Apps API in the command tests answered every `PATCH` as a
change of visibility. It now moves the app when the body names a slug.
