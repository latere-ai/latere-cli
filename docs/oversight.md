# Oversight

Limen is the platform's decision service. A step is one tool call of an
agent in a session. Before a step runs, the session's rules decide it. For
a step that the rules leave open, Limen predicts how likely you are to
allow it, from your own Allow and Deny answers.

In `learned` mode, Limen decides a step of a `progressive` session if it
has evidence about you. Elsewhere, the rules and the risk score decide,
and Limen only suggests. Limen has evidence if it suggests allow or block,
or if it asks with an evidence ratio of 0.5 or more (`split`). If it asks
because you gave too few answers (`unsure`), it has no evidence yet.

Limen gives `split` from the evidence ratio alone. Your answers can agree
and the ask is still `split`: for example, two allows of a publish give
80%, and `careful` runs a step on its own only above 90%. The commands
say that your answers disagree only if the answers that moved the
suggestion include an allow and a deny with the same label. A deny of a
different kind of step is not a disagreement about this step. Otherwise
the commands say `between`: the chance is between the thresholds of the
trust profile.

`latere oversight` shows what Limen recorded and learned about you, and
lets you correct it. It uses your `latere login`.

These commands are for you, and for a coding agent on your machine that
helps you debug. Nothing here is given to an agent inside a session.

```sh
latere oversight cases                     # the steps Limen holds, newest last
latere oversight cases --pending           # the steps waiting for you to check
latere oversight explain <action>          # why Limen suggested what it did
latere oversight learned                   # what Limen learned, per kind of step
latere oversight reliability               # how often Limen went against you
latere oversight profile                   # your trust profile
latere oversight profile set balanced      # choose careful, balanced or strict
latere oversight profile --agent <name>    # the profile that an agent uses
latere oversight profile set strict --for-me  # make your organization's profile stricter for you
latere oversight report                    # your organization's weeks, for owners and admins
latere oversight answer <action> allow     # allow or deny a step you checked
latere oversight forget                    # remove everything Limen holds about you
```

An `<action>` is one step of a session: the session id and the tool use
id, with a `/` between them, for example
`ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01`. `latere oversight cases` and
`latere agents session <id>` print them.

## Contexts

Limen keeps your records in a context. The subject of each record is your
subject: your issuer and your sign-in id, with a `|` between them. The
organization of the record is the context:

- Your own context. The organization is your subject.
- The context of an organization. The organization is the ID of the
  organization, for example `org_7`. The steps of an agent that the
  organization owns can be in this context.

Each step of a hosted session records the organization that its decision
was sent to Limen under (`decision_org`). `explain`, `answer` and `cases
--session` read the log of the session through the agents API, and use
this organization for each step. A step without it is in your own
context. Steps from before the platform recorded the organization have
none. If the organization of a step has a `|`, it is the context of a
person, and the command reads your own context.

`profile --agent`, `profile --for-me` and `report` use the context that
your login acts in. `latere login --org-id <id>` signs you in to an
organization, and `latere org` shows the context. If
`LATERE_OVERSIGHT_TOKEN` is a JWT, its `org_id` gives the context. The
other commands read your own context.

Limen answers for the context of an organization only if you log in to
that organization. If Limen refuses a step of an organization, the
command gives the login to use, for example `latere login --org-id
org_7`.

If the command cannot read the log of the session, it reads your own
context and writes a note to standard error. A session that the agents
API does not have is in your own context, with no note.

Every command takes `--json`. It prints the API's answer as it came.

## Cases

```sh
latere oversight cases
latere oversight cases --pending
latere oversight cases --session ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX --limit 0
```

A case is one step. Each row shows:

| Column | What it is |
|---|---|
| `ACTION` | The action id. Give it to `explain` and `answer`. |
| `STEP` | The kind and the name of the step, such as `tool bash`. |
| `SUGGESTED` | Limen's verdict: `allow`, `ask` or `block`. |
| `ALLOW` | The chance that you allow the step. |
| `UNCERTAINTY` | How unsure Limen is of that chance. |
| `ASK` | For an ask, the word of Limen: `unsure` (your answers say little yet) or `split` (Limen has enough of your answers; they can agree or disagree). `explain` tells which. |
| `DECIDED` | The verdict that was applied. |
| `SHOWN` | The chance that you were shown the step. 100% means it waited for you. |
| `BY` | What decided the step. Topos gives its mode and the layer that decided, for example `topos/progressive/score`. An older Topos gives only the mode, for example `topos/confirm`. |
| `ANSWERS` | Your answers. `(check)` marks the answer that checked a step you were shown. |

Limen keeps cases oldest first. The command reads all of them and shows
the latest `--limit`, 20 by default. `--limit 0` shows all.
`--pending` shows only the steps you were shown and did not check yet.

Without `--session`, the command shows the cases of your own context.
With `--session`, it reads the cases of each context that the steps of
the session are in. It shows them in the order of the log of the session.

## Explain

```sh
latere oversight explain ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01
```

The command reads the log of the session to find the context of the
step ([Contexts](#contexts)). The explanation shows:

- The suggestion, the chance that you allow the step, and the uncertainty.
- A scale from stop to run. The bars are your thresholds, and the star is
  the chance.
- For an ask, why Limen asks:
  - `unsure`: your answers say little about steps like this. More answers
    move the suggestion.
  - `split`: the answers that moved the suggestion include an allow and a
    deny with the same label. More answers do not move it much.
  - `between`: Limen has enough of your answers, and they agree, but the
    chance is between the thresholds of the trust profile. The line gives
    the chance, the profile, and the threshold that the chance does not
    reach, for example "not sure enough yet: Limen is 80% sure you would
    allow it, and Careful runs it on its own only above 90%". The profile
    is the preset nearest the thresholds of the explanation.
- The evidence ratio: how much of what Limen did not know your answers
  settled.
- The parts: how much comes from steps like this one, from your answers
  lately, and from both together.
- The model that explains, and how many answers it was fitted on. If a
  different model made the suggestion, Limen has learned since.
- For a step in the context of an organization, the trust profile that
  applied (`Profile`). The command reads it from the case of the step,
  because the explanation does not give it. It is the profile of the
  organization, the own profile of the agent, or "stricter, as you
  picked" if your own profile made it stricter for you. If the command
  cannot read the case, it does not show this line.
- The answers of yours that moved the suggestion most, each with its share.

## Learned

```sh
latere oversight learned
```

One row for each kind of step you answered: how many you allowed and
denied, the chance that you allow it now, the uncertainty, the scale, the
verdict now, and the ask kind. The ask kind is `split` only if you
allowed and denied steps of that kind. A `split` of Limen for a kind that
you only allowed or only denied shows as `between`. Under the table: the model, and the trend
of how much you let steps run lately, compared with what Limen assumes of
anyone.

## Reliability

```sh
latere oversight reliability
latere oversight reliability --of applied --from 2026-10-01
latere oversight reliability --of rules/1 --to 2026-11-01T00:00:00Z
```

The report shows two error rates, each with a 95% interval:

- Of the steps the source let run, the share you would have denied.
- Of the steps the source stopped, the share you would have allowed.

Each rate comes from the steps you were shown and checked. If you checked
none, the rate is unknown. Then the report says if your trust profile's
claim holds: how many checked steps in a row you agreed with, and how many
the claim needs.

`--of` names the source: `learned` (Limen, the default), `applied` (the
verdicts applied, whatever decided them), or a rule set such as
`rules/1`. `--from` and `--to` take a date or an RFC 3339 time. The
default window is the last 30 days.

## Profile

```sh
latere oversight profile
latere oversight profile set careful
```

Your trust profile sets when Limen lets a step run, asks you, or stops it.
`profile set` chooses one of the three profiles the console offers:

| Profile | Runs a step at | Stops a step below | Costs (wrong run / wrong stop / question) |
|---|---|---|---|
| `careful` | 90% | 25% | 10 / 4 / 1 |
| `balanced` | 65% | 35% | 2.857 / 2.857 / 1 |
| `strict` | 98% | 25% | 50 / 4 / 1 |

Each profile shows you 1 in 20 of the steps that run on their own, to
check. Everyone starts on `careful`.

### The own profile of an agent

```sh
latere oversight profile --agent release-notes
latere oversight profile set strict --agent release-notes
latere oversight profile reset --agent release-notes
```

An agent can have its own trust profile in a context. `--agent` names
the agent by its name or its id. The command reads the agent from the
agents API in the context of your login. Limen keeps the profile under
the id of the agent.

- `profile --agent` shows the profile that the agent uses: its own, or
  the profile of the context, which it follows.
- `profile set --agent` gives the agent its own profile. The profile
  applies to the sessions of all persons with that agent, from the next
  step. It does not change when the profile of the organization changes.
- `profile reset --agent` removes the own profile of the agent. The agent
  then follows the profile of the organization again, and its later
  changes.

In an organization, only an owner or an admin can set or remove the
profile of an agent. Limen refuses a different member. In your personal
context, the profile is yours.

### Your stricter profile in an organization

```sh
latere oversight profile --for-me
latere oversight profile set strict --for-me
latere oversight profile reset --for-me
```

A member can make the profile of their organization stricter for their
own sessions. It can only make Limen ask you more. It cannot make Limen
ask you less. For each of your steps, Limen uses the higher threshold to
run a step and the lower threshold to stop a step, from the two profiles.

- `profile --for-me` shows your pick, the profile of the organization,
  and what applies to your sessions. If your pick is not stricter, it
  changes nothing now. It applies if the organization picks a less strict
  profile later.
- `profile set --for-me` saves your pick. The sessions of other persons
  do not change. The owners and the admins of the organization cannot see
  your pick.
- `profile reset --for-me` removes your pick.

A forget in the organization, as the console offers it, also removes
your pick. `latere oversight forget` forgets your own context, and does
not remove your pick in an organization. `--for-me` needs a login in an
organization.

`profile reset` needs `--agent` or `--for-me`. To go back to the profile
where everyone starts, run `latere oversight profile set careful`.

## Report

```sh
latere oversight report
latere oversight report --from 2026-08-03 --to 2026-11-02
latere oversight report --json
```

The report of an organization shows how the steps of its agents went,
week by week, for all persons who used them. Only an owner or an admin of
the organization can read it. The command reads the organization of your
login.

The report never shows one person:

- A week shows 7 days after it ends, so that late checks count. Before
  that, the week is open.
- A week shows only if 5 or more persons used the agents in it. A smaller
  week says that it has too few people. It does not say how many.
- A number that comes mostly from the checks of one person shows as `not
  shown`.
- An agent that fewer than 5 persons used is in `Other agents`.
- A week that the report showed one time does not change.

Each row shows:

| Column | What it is |
|---|---|
| `WEEK` | The Monday that starts the week. |
| `PEOPLE` | The number of persons who used the agents in the week. |
| `RAN ON ITS OWN` | The steps that ran without a question: allowed, or flagged for a check. |
| `ASKED` | The steps that waited for a person. |
| `STOPPED` | The steps that were stopped. |
| `SHOULD HAVE ASKED` | Of the flagged steps that persons checked, how many a check said should have asked. |

After the weeks, the command shows the agents of the latest week that
shows its numbers. Then it shows two rates for that week: of the steps
that ran on their own, the share that the persons who checked them would
have stopped. One rate is for the suggestions of Limen. The other rate is
for the verdicts that were applied.

`--from` and `--to` give the weeks. Each is a Monday, as a date such as
`2026-10-05`, or a time at 00:00 UTC. They can be 13 weeks apart or
less. The default is the 5 weeks before the Monday that starts this
week. The command refuses other values before it calls Limen.

## Answer

```sh
latere oversight answer ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01 allow
latere oversight answer ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_02 deny
```

`allow` is "Looks fine" in the console: the step was fine to run. `deny`
is "Should have asked". Your first answer to a step you were shown counts
toward how often Limen is wrong. Every answer teaches Limen. The command
sends an answer once and does not send it again, because each answer that
Limen receives counts. The answer goes to the context of the step
([Contexts](#contexts)).

## Forget

```sh
latere oversight forget
latere oversight forget --yes
```

This removes all that Limen holds about you: your steps, your answers, and
what it learned from them. You cannot undo it. The command asks you to
type `yes`. `--yes` skips the question.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | The command is done. |
| `2` | The request was refused. The same command gets the same answer. Examples: you are not signed in, the id is not `<session>/<tool_use_id>`, Limen has no decision for the step, you are not an owner or an admin of the organization, a bound of the report is not a Monday. |
| `3` | No answer came, or the service was busy. The same command can succeed later. |

## Settings

| Setting | Default | Purpose |
|---|---|---|
| `LATERE_OVERSIGHT_URL` | `https://api.latere.ai/v1/oversight` | The oversight API, with its `/v1/oversight` path. `--api-url` overrides it. |
| `LATERE_OVERSIGHT_TOKEN` | none | A bearer to present instead of a token minted from your login. If it is a JWT, its subject is the person, and its `org_id` is the organization. |
| `LATERE_AGENTS_URL` | `https://api.latere.ai/v1/agents` | The agents API, with its `/v1/agents` path. `explain`, `answer` and `cases --session` read the log of a session from it, and `profile --agent` reads the agent. |
| `LATERE_AGENTS_TOKEN` | none | A bearer to present to the agents API instead of a token minted from your login. |
| `AUTH_URL` | from the API URL | The issuer. `--auth-url` overrides it. |
