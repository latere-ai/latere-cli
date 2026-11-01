---
title: "`latere oversight` and `latere agents sessions`: a person, and their coding agent, inspect what Limen recorded and learned about them, and how each tool call of a hosted session was decided"
status: implemented
depends_on:
  - specs/011-capability-commands.md
  - specs/015-evals-commands.md
  - "CROSS-REPO: Limen's oversight API at /v1/oversight; sdk-go oversight.ListCases, GetReliability, agents.Session.Policy and agents.ToolConfirmation (main after v0.23.0)"
  - "CROSS-REPO: topos specs/100-the-organization-a-session-acts-in.md: a session's org and a call's decision_org; sdk-go agents.Session.Org, agents.ToolUse.DecisionOrg and agents.Suggestion.AskKind (main after v0.24.0)"
affects:
  - go.mod (sdk-go main after v0.23.0, to be v0.24.0)
  - internal/commands/oversight.go, internal/commands/oversight_test.go
  - internal/commands/agents_sessions.go, internal/commands/agents_sessions_test.go
  - internal/commands/apierror.go (the exit codes spec 015 gave evals, shared), internal/commands/evals.go, internal/commands/exit.go
  - internal/commands/topos.go, internal/commands/root.go, internal/commands/capability_names_test.go
  - docs/oversight.md, docs/agents.md, docs/configuration.md, README.md, CHANGELOG.md
created: 2026-11-01
updated: 2026-11-02
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
| Limen serves cases, explanations, what it learned, reliability, profiles, answers and forget to a person's own token at `https://api.latere.ai/v1/oversight`, audience `api.latere.ai` | the oversight API; sdk-go `oversight` |
| Limen's owner policy lets a caller act on the cases, reports and profiles whose subject is its own rendered subject, `<issuer>\|<sub>` | the oversight API; sdk-go `oversight.Subject` |
| Topos keys a hosted session's records in Limen by the person who started it, in every context, so the console reads them with the person's subject as the organization | topos `internal/decisions/decisions.go`, `Decider.key` |
| A recorded `agent.tool_use` carries its verdict, mode, risk, review probability, attended, Limen's suggestion, and the layer and rule that decided it; Topos up to v0.67.0 does not record the suggestion's ask kind, and Topos main after v0.67.0 does | topos `session/event.go`, `AgentToolUse`, `Suggestion` |
| The agents API lists a person's sessions newest first, each with its approval policy, at `https://api.latere.ai/v1/agents`, audience `api.latere.ai` | topos `internal/server/sessions.go`, `listSessions` |

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
  org context is not refused. Superseded for a step of a hosted session
  by the step's `decision_org` (above).
- **No join with Limen in `agents session`.** A per-call or per-page join
  would cost a request per call, or every case of the person; the footer
  names the oversight commands instead.
- **No ask kind in the SDK's suggestion.** Topos up to v0.67.0 does not
  record it, and the SDK holds its models to the agents API's document, so
  the command reads it from the event when the event carries it.
  Superseded: sdk-go main after v0.24.0 reads `Suggestion.AskKind`.
- **The step's organization is read, not given.** A flag for the
  organization would make the person copy it from `agents session`. The
  log already names it, at the cost of one log read per command.

### The organization of a step (Topos spec 100)

Topos after v0.67.0 records on each `agent.tool_use` the organization
that its decision was sent to Limen under, `decision_org`, and on a
session the organization that owns its agent, `org`. With
`TOPOS_DECISIONS_ORG=agent`, a call of an organization's agent is keyed
by the organization's bare ID, so a read with the person's subject as the
organization misses it.

- `explain <action>` and `answer <action>` read the action's session log
  through the agents API and name the step's `decision_org` as the
  organization, or the person's subject for a step without one.
- `cases --session <id>` reads the session's log, lists cases in the
  person's own context and in each organization its steps name, and
  orders them as the log made them. `cases` without `--session`, and
  `learned`, `reliability`, `profile` and `forget`, stay in the person's
  own context.
- A session the agents API answers 404 for is the person's own, silently.
  A log that cannot be read is the person's own, with a note on stderr,
  so the command still answers as it did before.
- A 403 in an organization's context names `latere login --org-id <id>`:
  Limen answers for an organization only to a token minted in it.
- `agents session` names the session's `org` in its header, and a call's
  `decision_org` when it has no `|`, which tells an organization's ID from
  a person's subject.

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
| 8 | `explain` and `answer` name a step's `decision_org`, or the person's subject for a step without one or a session the agents API does not have; a 403 in an organization names `latere login --org-id` | `TestOversightStepInAnOrganization` |
| 9 | A log that cannot be read leaves the step in the person's own context, with a note | `TestOversightStepWhenTheSessionCannotBeRead` |
| 10 | `cases --session` reads each context the session's steps are in, in the log's order; `cases` alone reads the person's own | `TestOversightCasesOfASessionInAnOrganization` |
| 11 | `agents session` names the session's organization, and a call's when Limen keeps it in an organization | `TestAgentsSessionOfAnOrganizationsAgent` |

## Outcome

Built. Production returned the person's cases, what Limen learned, their
hosted sessions and one session's calls with a locally built binary and
the existing login. The release waits for sdk-go v0.24.0, which go.mod
then names in place of the pseudo-version.

Later, `docs/oversight.md` and the help say what Limen decides in
`learned` mode and define a step as one tool call, which the
`agents session` help and `docs/agents.md` use for the oversight
commands. They say that a session from before the platform recorded the
ask kind shows none.

On 2026-11-02, the oversight commands read a step of a hosted session in
the organization its `decision_org` names (criteria 8 to 11), on sdk-go
main after v0.24.0. The release pins the sdk-go release in place of the
pseudo-version. The docs give a decision's source in its three-part form,
`topos/<mode>/<decided_by>`.
