---
title: "`latere oversight` and `latere agents sessions`: a person, and their coding agent, inspect what Limen recorded and learned about them, and how each call of a hosted session was decided"
status: implemented
depends_on:
  - specs/011-capability-commands.md
  - specs/015-evals-commands.md
  - "CROSS-REPO: limen specs/009-explanations.md and its /v1/oversight route (v0.2.0); sdk-go oversight.ListCases, GetReliability, agents.Session.Policy and agents.ToolConfirmation (main after v0.23.0)"
affects:
  - go.mod (sdk-go main after v0.23.0, to be v0.24.0)
  - internal/commands/oversight.go, internal/commands/oversight_test.go
  - internal/commands/agents_sessions.go, internal/commands/agents_sessions_test.go
  - internal/commands/apierror.go (the exit codes spec 015 gave evals, shared), internal/commands/evals.go, internal/commands/exit.go
  - internal/commands/topos.go, internal/commands/root.go, internal/commands/capability_names_test.go
  - docs/oversight.md, docs/agents.md, docs/configuration.md, README.md, CHANGELOG.md
created: 2026-11-01
updated: 2026-11-01
author: changkun
---

# Oversight and hosted sessions

Limen, the platform's decision service, learns from a person's Allow and
Deny answers when to ask them about an agent's step. The console shows
what it learned. A person debugging a decision, and the coding agent on
their machine that helps them, need the same records as text and JSON,
with the login they already have.

## Current state

| Fact | Where |
|---|---|
| Limen serves cases, explanations, what it learned, reliability, profiles, answers and forget to a person's own token at `https://api.latere.ai/v1/oversight`, audience `api.latere.ai` | limen `server/server.go`, `routes`; deployment `LIMEN_OIDC_AUDIENCE=limen,api.latere.ai` |
| Limen's owner policy lets a caller act on the cases, reports and profiles whose subject is its own rendered subject, `<issuer>\|<sub>` | limen `server/auth.go`, `OwnerPolicy` |
| Topos keys a hosted session's records in Limen by the person who started it, in every context, so the console reads them with the person's subject as the organization | platform `frontend/src/console/oversight/api.ts` |
| A recorded `agent.tool_use` carries its verdict, mode, risk, review probability, attended, Limen's suggestion, and the layer and rule that decided it; Topos does not record the suggestion's ask kind | topos `session/event.go`, `AgentToolUse`, `Suggestion` |
| The agents API lists a person's sessions newest first, each with its approval policy, at `https://api.latere.ai/v1/agents`, audience `api.latere.ai` | topos `internal/server/sessions.go`, `listSessions`; deployment `TOPOS_OIDC_AUDIENCE=topos,api.latere.ai` |

## Design

`latere oversight` has seven commands, each with `--json`:

| Command | Calls |
|---|---|
| `cases [--pending] [--session ID] [--limit N]` | `GET /cases`, every page |
| `explain <session>/<tool_use_id>` | `GET /explanations` |
| `learned` | `GET /learned` |
| `reliability [--of S] [--from T] [--to T]` | `GET /reliability` |
| `profile`, `profile set careful\|balanced\|strict` | `GET`, `PUT /profiles` |
| `answer <session>/<tool_use_id> allow\|deny` | `POST /answers`, sent once |
| `forget [--yes]` | `POST /subjects/forget` |

Every call names the person as both the organization and the subject, in
every context, as Topos keys them. The person is read from the bearer the
command presents when it is a JWT, and otherwise from the saved login:
its `iss`, or the resolved issuer, and its `sub`.

Limen pages cases oldest first and has no session filter, so `cases`
reads every page at the largest size, keeps a session's by the action id's
prefix, and shows the latest `--limit`, 20 by default, 0 for all.

`reliability` defaults to Limen's own suggestions over the 30 days before
now, since Limen requires a window.

`profile set` writes the console's three presets, each with the console's
audit rate of 0.05: careful 10/4/1, balanced 2.857/2.857/1, strict 50/4/1.

`latere agents sessions` lists the person's hosted sessions newest first
with their mode. `latere agents session <id>` reads the whole log, lists
each `agent.tool_use` in order with how it was decided, joins the
`user.tool_confirmation` that answered it, and names `latere oversight
explain` and `latere oversight cases --session` for the why and the
person's checks. It reads a suggestion's `ask_kind` from the event when
the event carries it.

The commands share the exit codes spec 015 gave `latere evals`: 0 done, 2
refused, 3 no answer. The type and wording moved to `apierror.go`.

## Decisions

- **Org = subject in every context.** The directive named a person's own
  context, and Topos keys records by the person in every context, so an
  org context is not refused.
- **No join with Limen in `agents session`.** A per-call or per-page join
  would cost a request per call, or every case of the person; the footer
  names the oversight commands instead.
- **No ask kind in the SDK's suggestion.** Topos does not record it, and
  the SDK holds its models to the agents API's document.

## Not in this spec

- Previews of another profile (`POST /previews`).
- Profiles scoped to a kind or an agent.

## Acceptance criteria

| # | Criterion | Proved by |
|---|---|---|
| 1 | Every oversight call names the signed-in subject as org and subject, from the login or a JWT bearer | `TestOversightCases`, `TestOversightPerson` |
| 2 | `cases` reads every page, filters by session, keeps the latest `--limit`, and prints raw cases with `--json` | `TestOversightCases` |
| 3 | `explain`, `learned`, `reliability` and `profile` print the fields the directive names | `TestOversightExplain`, `TestOversightLearned`, `TestOversightReliability`, `TestOversightProfile` |
| 4 | `profile set` writes the presets' costs and audit rate | `TestOversightProfile` |
| 5 | `answer` is sent once; `forget` asks for yes unless `--yes` | `TestOversightAnswer`, `TestOversightForget` |
| 6 | A refusal exits 2 and an outage 3, with a 401 naming `latere login` | `TestOversightRefusals`, `TestOversightAnswer` |
| 7 | `agents sessions` and `agents session` print the fields the directive names, join confirmations, and name the oversight commands | `TestAgentsSessions`, `TestAgentsSession` |

## Outcome

Built. Production returned the person's cases, what Limen learned, their
hosted sessions and one session's calls with a locally built binary and
the existing login. The release waits for sdk-go v0.24.0, which go.mod
then names in place of the pseudo-version.
