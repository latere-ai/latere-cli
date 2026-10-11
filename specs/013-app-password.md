---
title: "`latere apps password`: a password opens a private app, `access` says whether one is set, and `visibility` removes it"
status: implemented
depends_on:
  - specs/009-repos-command.md
  - specs/010-app-command.md
  - specs/012-app-access.md
  - "CROSS-REPO: platform specs/193-an-app-opens-with-a-password.md and internal/web/apps_access.go, PUT and DELETE /v1/account/apps/{app_id}/access/password, and the access read's password, set_password and limits"
  - "CROSS-REPO: platform docs/apps/sharing.md, the three choices of who can open an app and the words for them"
affects:
  - internal/commands/app_password.go (new: password)
  - internal/commands/app_access.go (access says the choice and the password; visibility asks the platform and removes a password)
  - internal/commands/app.go (the group's help)
  - cmd/latere/app_password_e2e_test.go (new), cmd/latere/app_access_e2e_test.go
  - docs/apps.md, docs/configuration.md, docs/login-and-tokens.md, README.md, CHANGELOG.md
created: 2026-10-11
updated: 2026-10-11
author: changkun
---

# A password for a private app, from the CLI

A private app opened for its owner, an organization's members and the people
invited by email, each signed in with a Latere account (012). The owner asked
for a third way in: a password a visitor types, with no account. The platform
keeps the password beside the people invited, the gate asks for it, and the
chat, the console and the CLI each offer the same three choices in the same
words. This records the CLI's part.

## Current state

| Fact | Where |
|---|---|
| Who can open an app is one of three choices: Only people invited (`private`, no password), Anyone with the password (`private`, a password set), Anyone with the link (`unlisted`, and an app already `public`) | platform `docs/apps/sharing.md` |
| `PUT /v1/account/apps/{app_id}/access/password` with `{"password"}` sets or replaces the password, 204. `DELETE` on the same path removes it, 204 whether one was set or not. Both are decided as `app.settings`, like an invitation | platform `internal/web/apps_access.go` |
| `GET /v1/account/apps/{app_id}/access` answers `password`, `{"set_at"}` or `null`, names `set_password` among `actions` where the platform sets passwords, and carries `limits.min_password` and `limits.max_password` | platform `internal/web/apps_access.go` |
| A platform that sets no passwords answers `404 passwords_unconfigured` on both writes, leaves `set_password` out of `actions`, and answers `password: null` whatever the app has | platform `internal/web/apps_access.go` |
| A password outside the bounds is `400 invalid_field` with `details.field` `password`, `details.reason` `length`, `details.min` and `details.max`. A password is counted in Unicode code points, with nothing trimmed | platform `internal/web/apps_access.go` |
| The platform keeps a password in a form it cannot read back, and no answer carries it | platform spec 193 |
| A change of visibility at the Apps API does not touch the password. One left on an app anyone with the link opens does nothing, and opens the app again once it is private | platform `docs/apps/sharing.md` |
| A generated password is 4 groups of 5 characters from `abcdefghjkmnpqrstuvwxyz23456789`, joined by `-`, from a cryptographic source, on every client | the contract of 2026-10-11 that the platform, the gate and the clients build against |

## Design

### Commands

| Command | Routes | Behavior |
|---|---|---|
| `password [slug]` with `--generate`, `--stdin` or a prompt, and `--json` | `GET /apps/{slug}`, `GET .../access`, `PATCH /apps/{slug}` with `{visibility: private}` when the app is not private, then `PUT .../access/password` | sets or replaces the password; prints the app, that the password is set or replaced, who can open the app, that the app was made private when it was, and the generated password alone on the last line |
| `password --remove [slug]` with `--json` | `GET /apps/{slug}`, `GET .../access`, then `DELETE .../access/password` when one is set | says the password was removed and what that changes, or that the app had none, exit 0 either way |
| `access [slug]` | as 012 | the first lines name the choice the app is on; a password is said with the moment it was set |
| `visibility <value> [slug]` | `GET /apps/{slug}`, `GET .../access`, `PATCH /apps/{slug}`, then `DELETE .../access/password` when one is set | as 012, and removes a password the app has, saying so; with `--json` the removal is said on standard error |
| `list`, `show` | as 012 | unchanged: neither reads the platform, so neither says a password |

### Where the password comes from

A password is never a flag's value or an argument: a shell history and a
process list keep both. It comes from the first of `--generate`, `--stdin`
and a prompt.

`--generate` draws each character with `crypto/rand` through `rand.Int`,
which rejects the values a remainder would favor, so every character of the
alphabet is as likely as another. The value is printed once, on the last
line of the output, or as `password.generated` under `--json`.

`--stdin` reads the first line of standard input and removes its line
ending, LF or CRLF, and nothing else. The read is bounded, so a file piped
by mistake is not read whole.

The prompt reads from the terminal with `golang.org/x/term`, which the
module already depends on, without echo, twice, and refuses two answers that
differ: nothing typed is shown, so a slip would set a password no one knows.
The prompts go to standard error. Without a terminal, `--stdin` or
`--generate`, the command refuses before any request and names the two
flags.

A password that was typed or piped is in no output and no error. Three
places could repeat one and do not: more than one argument is refused
without naming any; a flag pflag cannot read is refused with a sentence of
the command's own, since pflag's quotes what it was given; and an answer to
the set that is not the platform's envelope, such as a proxy's page, keeps
only its status.

### The order of a set

1. At a terminal the password is asked for, before any request. The
   terminal shows what is typed until the prompt reads, so a prompt that
   waited for the platform would show whatever a person typed ahead of it.
2. The app is read, then the access answer.
3. `actions` without `set_password` ends the command with
   `passwords_unconfigured` and the sentence that passwords are not available
   on this platform yet, exit 1.
4. A password that was not typed is generated or read, and the password is
   held to `limits` from the access answer, counted in Unicode code points.
   A bound the answer leaves out is the platform's to hold. Text that is not
   UTF-8 is refused: it would be sent as other characters than were given.
5. The app is made private when it is not, through the change `visibility`
   makes.
6. The password is set.

Everything that can refuse without the platform's writes comes before step
5, so a refusal there changes nothing. A set that fails after step 5 leaves
the app private, and the failure says so with the command that opens it
again.

### `visibility` and a password

`private` means Only people invited and `unlisted` means Anyone with the
link. Neither is the choice a password makes, so both remove a password the
app has. `visibility` therefore asks the platform before it changes
anything, and a platform that does not answer leaves the app as it was. The
password is removed after the change. A removal that fails is reported after
the command has printed what the change left, with the password still named
as the app's, exit 1.

On a platform that sets no passwords the access answer says of every app
that it has none, and `visibility` changes the visibility alone.

### What `access` says

The general-access sentence starts with the choice in the words every
surface uses, then says what it lets in. Anyone with the password carries
the moment the password was set. A password on an app anyone with the link
opens is said on lines of its own, with the command that removes it.
`--json` is unchanged in shape: `access` is the platform's own bytes, which
carry `password`, `actions` and `limits`.

### `--json` on `password`

`{"app": <the app as the API answers it>, "password": {"set": true}}`, with
`"generated"` holding the value only when `--generate` made it. `--remove`
prints `{"set": false}`. The app is the answer of the change that made it
private when there was one.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| Where a password is given | a prompt, `--stdin`, `--generate` | a `--password` flag or an argument: both land in the shell history and the process list |
| The order of a set | the two reads, the password, the change to private, the set | the set first: a failure of the change after it would leave a password on an app anyone with the link opens, and `visibility private` to finish the job would remove the password again |
| When a typed password is asked for | first, before any request | after the two reads, which could say that the platform sets no passwords before anything was typed: the terminal shows what is typed until the prompt reads, and the reads take long enough to type ahead of |
| The bounds of a password | read from `limits` of the access answer | constants in the CLI: a second copy of the platform's two numbers |
| `--remove` of no password | no request, exit 0, says there was none | exit 1: the state the person asked for already holds |
| `visibility` when the platform does not answer | nothing changes | changing the visibility and reporting the password afterwards: `private` would report success on an app a password still opens |
| When the password is removed by `visibility` | after the change | before it: a refused change would have removed the password of an app that stays as it was |
| A removed password under `visibility --json` | one line on standard error | a field in the output: standard output is the app as the API answers it |
| `show` and `list` | say nothing of a password | one platform read for each row of `list`, and a second API for `show`, to print one word |
| The prompt's test | a function the suite stands in for | a pseudo-terminal in the suite: the repository has no helper for one |

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | A generated password has the shape every client makes, and two differ | `TestGeneratedPasswordShape`, `TestGeneratedPasswordsDiffer` |
| 2 | `--generate` sets the password and prints it once, alone on the last line | `TestAppPasswordGeneratesOneAndPrintsItOnce`, `TestAppPasswordE2E` |
| 3 | `--stdin` sets the first line without its line ending and with nothing else removed, and prints it nowhere | `TestAppPasswordFromStandardInput`, `TestAppPasswordE2E` |
| 4 | A terminal is asked twice on standard error before any request, two answers that differ set nothing, and nothing typed is printed | `TestAppPasswordTypedAtATerminal`, `TestAppPasswordIsAskedForBeforeAnyRequest`, `TestAppPasswordTypedTwoWays`, `TestAppPasswordPromptThatEnds` |
| 5 | Without a terminal, `--stdin` or `--generate` the command refuses before any request and names the two flags; a password as an argument or a flag's value is refused and not repeated | `TestAppPasswordWithoutATerminal`, `TestAppPasswordIsNeverAnArgument`, `TestAppPasswordE2E` |
| 6 | An app that is not private is made private before the password is sent, and the command says so | `TestAppPasswordMakesTheAppPrivateFirst`, `TestAppPasswordOfAnAppThatCannotBeMadePrivate` |
| 7 | A password outside the bounds the access answer names is refused before any write, counted in code points; the platform's own refusal is said with its bounds | `TestAppPasswordIsHeldToTheBoundsThePlatformNames`, `TestAppPasswordRefusedByThePlatformForItsLength` |
| 8 | A platform that sets no passwords is said so, exit 1, with the app unchanged | `TestAppPasswordWhereThePlatformSetsNone`, `TestAppPasswordRefusedWhereThePlatformSetsNone`, `TestAppPasswordE2E` |
| 9 | No refusal and no answer repeats a password that was typed or piped | `TestAppPasswordRefusals`, `TestAppPasswordRefusedByThePlatformForItsLength` |
| 10 | `--remove` removes a password, says what that changes, and succeeds with no request when there is none | `TestAppPasswordRemove`, `TestAppPasswordRemoveRefusals`, `TestAppPasswordRemoveTakesNoPassword` |
| 11 | `--json` carries a password only when `--generate` made it, once | `TestAppPasswordJSON` |
| 12 | `access` names the choice the app is on, a password with its moment, and one left on an app anyone with the link opens; `--json` carries the platform's `password` | `TestAppAccessByVisibility`, `TestAppAccessJSONSaysWhetherAPasswordIsSet` |
| 13 | `visibility` removes a password with either value and says so, on standard error under `--json` | `TestAppVisibilityRemovesAPassword`, `TestAppVisibilityJSONSaysARemovedPasswordOnStderr`, `TestAppPasswordE2E` |
| 14 | `visibility` changes nothing without the platform's answer, changes the visibility alone where the platform sets no passwords, and reports a password it could not remove | `TestAppVisibilityChangesNothingWithoutThePlatformsAnswer`, `TestAppVisibilityWhereThePlatformSetsNoPasswords`, `TestAppVisibilitySaysAPasswordItCouldNotRemove` |
| 15 | The help says the generated password is printed once and names the words of no mechanism | `TestAppPasswordHelp`, `TestAppWithoutASubcommandShowsHelp` |

## Outcome

Built on 2026-10-11, not released. Every criterion has a passing test, and
the prompt was run once on a real terminal with the built binary: nothing
typed was shown, and two answers that differed set nothing.

`visibility` now reads the platform before it changes an app, where 012 had
it reach the Apps API alone. A script that gives it `LATERE_APPS_TOKEN`
needs `LATERE_PLATFORM_TOKEN` or a saved login as well, and a deployment
whose platform does not serve the access routes refuses the change.

`show` prints who a private app's visibility lets in and cannot say that a
password opens it too. Its help and the documentation point at `access`.

`term.ReadPassword` stops the terminal showing what is typed only while it
reads. The prompt comes before any request, which leaves the moment the
command starts and the moment between the two prompts as the only times a
key pressed is shown.
