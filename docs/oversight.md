# Oversight

Limen is the platform's decision service. A step is one tool call of an
agent in a session. Before a step runs, the session's rules decide it. For
a step that the rules leave open, Limen predicts how likely you are to
allow it, from your own Allow and Deny answers.

In `learned` mode, Limen decides a step of a `progressive` session if it
has evidence about you. Elsewhere, the rules and the risk score decide,
and Limen only suggests. Limen has evidence if it suggests allow or block,
or if it asks because your answers disagree (`split`). If it asks because
you gave too few answers (`unsure`), it has no evidence yet.

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
latere oversight answer <action> allow     # allow or deny a step you checked
latere oversight forget                    # remove everything Limen holds about you
```

An `<action>` is one step of a session: the session id and the tool use
id, with a `/` between them, for example
`ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01`. `latere oversight cases` and
`latere agents session <id>` print them.

Each command reads your own records. The organization and the subject are
both your subject: your issuer and your sign-in id, with a `|` between
them. This is true in every context, because Limen keeps the records of a
hosted session by the person who started it.

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
| `ASK` | For an ask: `unsure` (your answers say little yet) or `split` (your answers disagree). |
| `DECIDED` | The verdict that was applied. |
| `SHOWN` | The chance that you were shown the step. 100% means it waited for you. |
| `BY` | What decided: a rule set such as `topos/confirm`, or Limen's model. |
| `ANSWERS` | Your answers. `(check)` marks the answer that checked a step you were shown. |

Limen keeps cases oldest first. The command reads all of them and shows
the latest `--limit`, 20 by default. `--limit 0` shows all.
`--pending` shows only the steps you were shown and did not check yet.

## Explain

```sh
latere oversight explain ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01
```

The explanation shows:

- The suggestion, the chance that you allow the step, and the uncertainty.
- A scale from stop to run. The bars are your thresholds, and the star is
  the chance.
- For an ask, why Limen asks: `unsure` or `split`. More answers move an
  unsure ask. They do not move a split ask.
- The evidence ratio: how much of what Limen did not know your answers
  settled.
- The parts: how much comes from steps like this one, from your answers
  lately, and from both together.
- The model that explains, and how many answers it was fitted on. If a
  different model made the suggestion, Limen has learned since.
- The answers of yours that moved the suggestion most, each with its share.

## Learned

```sh
latere oversight learned
```

One row for each kind of step you answered: how many you allowed and
denied, the chance that you allow it now, the uncertainty, the scale, the
verdict now, and the ask kind. Under the table: the model, and the trend
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

## Answer

```sh
latere oversight answer ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01 allow
latere oversight answer ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_02 deny
```

`allow` is "Looks fine" in the console: the step was fine to run. `deny`
is "Should have asked". Your first answer to a step you were shown counts
toward how often Limen is wrong. Every answer teaches Limen. The command
sends an answer once and does not send it again, because each answer that
Limen receives counts.

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
| `2` | The request was refused. The same command gets the same answer. Examples: you are not signed in, the id is not `<session>/<tool_use_id>`, Limen has no decision for the step. |
| `3` | No answer came, or the service was busy. The same command can succeed later. |

## Settings

| Setting | Default | Purpose |
|---|---|---|
| `LATERE_OVERSIGHT_URL` | `https://api.latere.ai/v1/oversight` | The oversight API, with its `/v1/oversight` path. `--api-url` overrides it. |
| `LATERE_OVERSIGHT_TOKEN` | none | A bearer to present instead of a token minted from your login. If it is a JWT, its subject is the person. |
| `AUTH_URL` | from the API URL | The issuer. `--auth-url` overrides it. |
