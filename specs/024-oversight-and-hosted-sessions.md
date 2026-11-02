---
title: "`latere oversight` and `latere agents sessions`: a person, and their coding agent, inspect what Limen recorded and learned about them, and how each tool call of a hosted session was decided"
status: implemented
depends_on:
  - specs/011-capability-commands.md
  - specs/015-evals-commands.md
  - "CROSS-REPO: Limen's oversight API at /v1/oversight; sdk-go oversight.ListCases, GetReliability, agents.Session.Policy and agents.ToolConfirmation (main after v0.23.0)"
  - "CROSS-REPO: topos specs/100-the-organization-a-session-acts-in.md: a session's org and a call's decision_org; sdk-go agents.Session.Org, agents.ToolUse.DecisionOrg and agents.Suggestion.AskKind (main after v0.24.0)"
  - "CROSS-REPO: platform specs/243-oversight-in-an-organization-the-extras.md and Limen specs/010-organizations.md: an agent's own profile, a member's stricter profile and an organization's weekly report; sdk-go oversight.DeleteProfile, Scope.Subject, GetReport, Prediction.Profile and agents.GetAgent (main after v0.25.0)"
affects:
  - go.mod (sdk-go main after v0.23.0, to be v0.24.0)
  - internal/commands/oversight.go, internal/commands/oversight_test.go
  - internal/commands/oversight_profile.go, internal/commands/oversight_report.go, internal/commands/oversight_org_test.go
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

`latere oversight` has eight commands, each with `--json`:

| Command | Calls |
|---|---|
| `cases [--pending] [--session ID] [--limit N]` | `GET /cases`, every page |
| `explain <session>/<tool_use_id>` | `GET /explanations` |
| `learned` | `GET /learned` |
| `reliability [--of S] [--from T] [--to T]` | `GET /reliability` |
| `profile`, `profile set careful\|balanced\|strict`, `profile reset` | `GET`, `PUT`, `DELETE /profiles` |
| `report [--from T] [--to T]` | `GET /reports` |
| `answer <session>/<tool_use_id> allow\|deny` | `POST /answers`, sent once |
| `forget [--yes]` | `POST /subjects/forget` |

Every call names the person as both the organization and the subject, in
every context, as Topos keys them, except a step of an organization's
agent and the organization's own profiles and report (both below). The person is read from the bearer the
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
  organization when it is an organization's ID, and the person's subject
  for a step without one or with a person's context (a `|`), however
  Topos rendered that subject.
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

### In an organization: an agent's own profile, a member's stricter one, and the report (platform spec 243)

Limen spec 010 adds a route that removes a saved profile, a profile
scope with a person, and an organization's weekly report. Platform spec
243 gives them to an organization's owners, admins and members. The
command line gets them as flags of `profile` and a new command:

| Command | Calls |
|---|---|
| `profile --agent <name>` | `GET /agents/{ref}` on the agents API, then `GET /profiles?org&agent` |
| `profile set <preset> --agent <name>` | the same agent read, then `PUT /profiles` with `{org, agent}` |
| `profile reset --agent <name>` | the same agent read, then `DELETE /profiles?org&agent`, then `GET` for what applies now |
| `profile --for-me` | `GET /profiles?org&subject`, and `GET /profiles?org` when the person saved a pick |
| `profile set <preset> --for-me` | `PUT /profiles` with `{org, subject}`, then `GET /profiles?org` for what the pick does now |
| `profile reset --for-me` | `DELETE /profiles?org&subject` |
| `report [--from T] [--to T]` | `GET /reports?org&from&to` |

- **The context is the login's.** The organization is the `org_id` of the
  bearer the command presents: `LATERE_OVERSIGHT_TOKEN` when it is a JWT,
  else the saved login. `--for-me` and `report` in the person's own
  context are refused before any call, with `latere login --org-id <id>`.
  `--agent` in the person's own context is the agent's own profile under
  the person's subject, which Limen keys a person's own agent's steps by.
- **An agent by its name.** Limen keys an agent's steps by the agent's id
  (`actor.id`, which Topos fills from the session's agent id). The
  command reads the agent from the agents API, by its name in the login's
  context or by its id, through sdk-go's `agents.GetAgent`, and names the
  agent's `status.id` in the scope. An agent the API does not answer is
  refused before Limen is asked.
- **What applies to the member.** `GET /profiles` with `subject` answers
  what the member picked, not what applies. The command reads the
  organization's profile too and takes the higher run threshold and the
  lower stop threshold, as Limen's `Stricter` does, to say whether the
  pick changes anything now.
- **The report** defaults to the console's window: the 5 weeks before the
  Monday that starts this week. A bound that is not a Monday at 00:00 UTC,
  bounds out of order, and more than 13 weeks are refused before any
  call. It prints each week (too few people, open with the day it shows,
  or its counts), the agents of the latest ready week with Other agents,
  and that week's two false-allow rates, `not shown` where withheld.
- **Whose profile applied.** `explain` of a step in an organization's
  context also reads the step's case (`GET /cases?action_id&limit=1`) and
  names the scope of the profile its prediction recorded, which the
  explanation does not carry: a `subject` is "stricter, as you picked",
  an `agent` the agent's own, and otherwise the organization's. A step in
  the person's own context reads no case.
- **Refusals.** A 403 on a change of an organization's agent's profile
  says that only an owner or an admin can make it, and a 403 on `report`
  that only an owner or an admin reads it. Every other refusal is
  `apiRefused`'s, with the exit codes above.

## Not in this spec

- Previews of another profile (`POST /previews`).
- Profiles scoped to a kind of step.
- A profile of a member for one agent: Limen keeps one per member and
  organization (Limen spec 010, Decisions 5).

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
| 12 | `profile --agent`, `profile set --agent` and `profile reset --agent` read the agent by its name and scope the profile by its id in the login's context; a 403 on a change names the owners and admins; an unknown agent is refused before Limen is asked | `TestOversightAgentProfile`, `TestOversightAgentProfileInThePersonsOwnContext` |
| 13 | `profile --for-me`, `set --for-me` and `reset --for-me` scope the profile by the person's subject in the organization, and say what applies: the pick, nothing now, or the stricter of each threshold | `TestOversightProfileForMe` |
| 14 | `--for-me` and `report` in the person's own context are refused with `latere login --org-id <id>`, before any call | `TestOversightOrganizationOnlyInAnOrganization` |
| 15 | `report` reads the 5 weeks before this week's Monday by default, refuses bounds that are not Mondays, out of order or more than 13 weeks apart, prints each week's state or counts, the latest ready week's agents and rates with withheld numbers as not shown, and says who reads it on a 403 | `TestOversightReport`, `TestOversightReportTooFew`, `TestMondayOf` |
| 16 | `explain` of a step in an organization names whose profile applied from its case's prediction, and leaves the line out where the case is not read or the step is the person's own | `TestOversightExplainNamesTheProfileThatApplied` |
| 17 | An ask that Limen calls `split` reads as split in `explain` only where the answers that moved it include an allow and a deny with the same label, and in `learned` only for a kind both allowed and denied; otherwise it reads as `between`, and `explain` gives the chance, the preset nearest the thresholds and the threshold it falls short of, as the console words it | `TestOversightAskWhoseAnswersAgree`, `TestOversightExplain` |

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

On 2026-11-02, the commands give an organization's agent its own trust
profile and take it back, let a member make the organization's profile
stricter for their own sessions, read the organization's weekly report,
and name whose profile applied to a step in `explain` (criteria 12 to
16, platform spec 243, Limen spec 010). They are built on sdk-go main
after v0.25.0, for `oversight.DeleteProfile`, `Scope.Subject`,
`GetReport`, `Prediction.Profile` and `agents.GetAgent`. The release
pins the sdk-go release in place of the pseudo-version.

On 2026-11-02, after the owner's live test, `explain` and `learned` stop
saying that answers that all went one way disagree (criterion 17). Limen
calls an ask `split` from its evidence ratio alone, so two allows of a
publish at 80% under `careful` were `split`. The commands now say `split`
only for an allow and a deny with the same label, since a deny of another
kind is no disagreement about this step, and `between` otherwise, with
the chance, the profile and the threshold it falls short of, in the
console's words: "not sure enough yet: Limen is 80% sure you would allow
it, and Careful runs it on its own only above 90%". `cases` shows the word of Limen, which the docs now
describe correctly. No call changes.
