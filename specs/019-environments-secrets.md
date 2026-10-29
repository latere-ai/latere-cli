---
title: "`latere environments secrets`: create, rotate, apply, list, get and delete the credentials a workload uses without holding them"
status: implemented
depends_on:
  - specs/008-cella-at-the-origin.md
  - specs/011-capability-commands.md
  - "CROSS-REPO: cella client/secrets.go (CreateSecret, ApplySecret, GetSecret, ListSecrets) and manifest/v1 Secret, at the v0.21.0 the CLI requires"
  - "CROSS-REPO: platform docs/environments/console.md, section Secrets: the five ways Add a secret offers and the fields each writes"
  - "CROSS-REPO: platform internal/authz/cella.go: a Secret's update is admitted only when it keeps the stored reserved labels"
affects:
  - internal/commands/cella_secrets.go (new: the secrets group)
  - internal/commands/cella.go (the group registered; apply points a Secret manifest at secrets apply)
  - internal/commands/cella_secrets_test.go (new), cella_core_test.go, capability_names_test.go
  - cmd/latere/cella_core_e2e_test.go
  - docs/environments.md, README.md, CHANGELOG.md
created: 2026-10-29
updated: 2026-10-29
author: changkun
---

# Secrets from the CLI

A person reported that `latere environments` had no way to create a secret,
so they called the API by hand before they could give a workload a
credential, and a create without the account's labels was refused. The core
serves the Secret kind, the exported client has its four calls, and the
console offers it as **Add a secret**. This records the CLI's part.

## Current state

| Fact | Where |
|---|---|
| `POST /v1/secrets` creates, and a name in use is `409 name_taken` | cella `internal/api/secrets.go` |
| `PUT /v1/secrets/{name}` creates, or replaces the whole object; an update without `spec.value` keeps the stored value | cella `internal/api/secrets.go`, `manifest/secret.go` |
| No answer carries a value | cella `manifest.StripSecretValue` |
| The platform admits a create only with the tenant, principal and, in an organization, org labels, and an update only when its reserved labels equal the stored ones | platform `internal/authz/cella.go` |
| The console offers five ways and maps each onto `kind`, `inject` and `oauth`; a basic or OAuth value is `left:right` | platform `docs/environments/console.md`, `frontend/src/console/environments/credentials.ts` |

## Design

### Commands

| Command | Does |
|---|---|
| `secrets create <name> --host H` | A create from flags: `--type bearer\|header\|basic\|query\|oauth` (default `bearer`), `--host` (repeatable, or commas), `--port` (443 unless given), `--header`, `--query`, `--username`, `--client-id`, `--token-url`, `--token-scope`, `--token-audience`, `--body`, `--json` |
| `secrets rotate <name\|id>` | Replaces the value; `--username` or `--client-id` again for a two-part value |
| `secrets apply -f <file>` | A `kind: Secret` manifest with the account's labels added, under the name it declares, as `latere environments apply` sends a workload's |
| `secrets list`, `get <name\|id>`, `delete <name\|id>` | Records, or the API's objects with `--json`; `get` prints JSON; `delete` says how many workloads attached the secret |

Every command takes `--api-url`. The environment variable a workload reads
the placeholder from is the workload's to name, in `spec.secrets[].env`, so
no command here takes one.

### Where a value comes from

Never a flag or an argument. With a terminal on standard input, a prompt on
standard error reads it without echo (`typedSecret`, shared with `apps
password`); otherwise standard input is read whole, bounded by the core's
`manifest.MaxSecretValueBytes`, with one trailing line ending removed. For
`basic` and `oauth` the value read is the password or the client secret,
joined to `--username` or `--client-id` by a colon, as the console joins its
two fields. A refused argument count or flag names none of what was given.

### A rotation

`rotate` reads the secret and sends back the object as read, its status
removed and `spec.value` set, as the console's Rotate does. A `PUT` with the
value alone would be refused: it replaces the whole spec, and the platform
compares the update's reserved labels with the stored ones. The type that
decides whether a half is needed is read from the stored spec.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| The create's route | `POST`, so a name in use is refused with the rotate command | `PUT`: a second `create` would replace a secret's spec and value unasked |
| The halves of a two-part value | the non-secret half as a flag, the secret half read | `user:password` on standard input: one more shape to explain, and the console asks for the two apart |
| How often a value is typed | once | twice, as `apps password`: a mistyped secret fails requests and `rotate` replaces it, where a mistyped app password locks visitors out |
| The console's **I understand who can use this secret** | the help says it, and `create` prints it in an organization | a confirmation or `--yes`: the organization was chosen with `latere org` |
| A Secret manifest given to `latere environments apply` | refused, naming `secrets apply` | routing it: two commands for one object, and `--wait` means nothing to a secret |

## Not in this spec

Attaching a secret, which is the workload manifest's `spec.secrets`. The
console's **Yours** and **Agent sessions** filters. Changing hosts or ports
from flags, which `apply -f` does.

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | A create carries the account's labels and the value from standard input, and no output repeats it | `TestSecretsCreateCarriesLabelsAndTheValueFromStdin`, `TestEnvironmentsSecretsOnTheCoreE2E` |
| 2 | Each `--type` writes the spec the console writes, and a two-part value is joined | `TestSecretsCreateMapsEachTypeAsTheConsole` |
| 3 | At a terminal the value is typed at a prompt that never shows it | `TestSecretsCreateReadsTheValueAtAPrompt` |
| 4 | Flags that cannot make a secret, no value, a value too long, or a value as an argument are refused before any request, without repeating it | `TestSecretsCreateRefusesBeforeSending` |
| 5 | A refused create ends with the core's sentence; a name in use is never replaced | `TestSecretsCreateRefusedSurfacesTheServersMessage`, `TestSecretsCreateNeverReplacesAStoredSecret` |
| 6 | A rotation keeps the stored labels and spec, drops the status and sets the value | `TestSecretsRotateKeepsEverythingButTheValue`, `TestSecretsRotateTwoPartValues`, `TestEnvironmentsSecretsOnTheCoreE2E` |
| 7 | `apply` labels the manifest and keeps its YAML; each apply points the other kind at its command | `TestSecretsApply` |
| 8 | `list`, `get` and `delete` | `TestSecretsListAndGet`, `TestSecretsDelete`, `TestSecretSentence` |

## Outcome

Built on 2026-10-29, not released. Every criterion has a passing test.
