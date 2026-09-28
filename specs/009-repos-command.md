---
title: "`latere repos`: create, list and look up repositories on platformd's repository routes"
status: implemented
depends_on:
  - specs/001-auth-unification-migration.md
  - "CROSS-REPO: platform internal/repositories, the person routes POST /repositories and GET /repositories on platformd's public listener"
  - "CROSS-REPO: auth deploy/base/clients.yaml, the latere-cli row's actor_audiences, which must list api.latere.ai"
affects:
  - internal/commands/repos.go (new)
  - internal/commands/root.go (the command group)
  - cmd/latere/repos_e2e_test.go (the built binary against a stub of the routes)
  - .lateregate.yaml (the identity block's audience)
  - docs/repos.md, docs/configuration.md, docs/login-and-tokens.md, README.md, CHANGELOG.md
created: 2026-09-28
updated: 2026-09-28
author: changkun
---

# `latere repos`

A person's script or a local agent needs to create a repository to push to.
Latere Code's own create route refuses a person's token by design: platformd
is the one writer, which records a repository and creates it at Latere Code
together, on its public route `POST /repositories`. The console already
creates there; the CLI had no way to.

## Design

### Address and credential

| Setting | Value |
|---|---|
| Base URL | `https://platform.latere.ai`, the host platformd's public routes answer on; `LATERE_PLATFORM_URL` or `--platform-url` overrides it |
| Bearer | an actor token minted at auth for the audience `api.latere.ai`, platformd's `AUTH_AUDIENCE`; `LATERE_PLATFORM_TOKEN` presents a bearer as given |
| Issuer | inferred from the base URL's host (`platform.latere.ai` gives `auth.latere.ai`), or `AUTH_URL`, or `--auth-url` |

`LATERE_PLATFORM_TOKEN` is the static-bearer escape every product command
has. It is also how a machine with no login reaches the routes: a sandbox
whose egress gateway swaps a placeholder for a token presents the
placeholder as this variable, and the command runs unchanged.

### Commands

| Command | Route | Behavior |
|---|---|---|
| `create <owner>/<name> [--public] [--json]` | `POST /repositories` with `{id, owner_label, slug, visibility}` | the id is a lower-case version 4 UUID chosen per run; visibility is `private` unless `--public`; prints the repository, its id, both clone URLs and the push commands, or the server's answer with `--json` |
| `list [--json]` | `GET /repositories` | the context's repositories and, in the personal context, those shared with the person, as a table of name, visibility, role and age |
| `get <owner>/<name> [--json]` | `GET /repositories`, filtered | the row whose owner label and slug match without case, among the same two lists; platformd has no lookup by name, and Latere Code's lookup would need a second audience for one read |

A name that is not `<owner>/<name>` is refused before any request. Every
other rule (the owner the person may create under, the name's shape, the
owner's cap) is the server's, and its sentence is what the person reads.

### Errors

platformd's repository routes answer `{"error": "<code>", "message",
"detail"}`, where `error` is a string, so the shared `api.APIError`, which
reads `code`, would lose the code. The command reads that document and the
shared envelope `{"error": {"code", "message", "details"}}` alike, prints
`<code>: <message>` with a `detail:` line when there is one, and exits 1. A
401 adds the line that says to run `latere login`.

Clone URLs name `CODE_HOST` when it is set, the host the git credential
helper answers for.

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | `create` sends the one writer's body with a fresh lower-case UUID per run, private by default, public with `--public` | `TestReposCreateSendsTheOneWritersBody`, `TestReposCreateChoosesAFreshIDEachTime` |
| 2 | `list` and `get` read `GET /repositories`, and `get` matches owner and name without case across owned and shared rows | `TestReposListPrintsTheContextAndWhatIsShared`, `TestReposGetFindsByNameWithoutCase`, `TestReposGetOfARepositoryOutsideTheContext` |
| 3 | A refusal carries the server's code, sentence and detail in either envelope, and exits 1 with nothing on stdout | `TestReposRefusalsCarryTheServersCodeAndSentence`, `TestReposE2E` |
| 4 | The bearer is an actor token for `api.latere.ai`, never the login token, unless `LATERE_PLATFORM_TOKEN` is set | `TestReposMintsForThePlatformsAudience`, `TestReposCreateSendsTheOneWritersBody` |
| 5 | `--json` prints the server's answer, and an empty context's lists as empty arrays | `TestReposCreateJSONIsTheServersAnswer`, `TestReposListOfAnEmptyContext` |

## Outcome

Built on 2026-09-28, not released. Every criterion has a passing test. The
commands reach production once auth's registry lists `api.latere.ai` among
the `latere-cli` client's actor audiences; until then the mint is refused
`invalid_target`.
