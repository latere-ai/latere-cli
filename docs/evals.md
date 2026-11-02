# Evals

`latere evals` reads what you keep in Evals, the platform's experiments on
agents and on programs built on models: the suites, tasks, metrics, graders
and candidates of your current context, and the experiments run on them. It
shows an experiment's progress and follows one to its end, so a script or a
CI job can wait for an experiment the console started and act on how it
ended.

The console at <https://platform.latere.ai/console/evals> is where you
create and run experiments and read their results, comparisons, runs and
the files each run made. Every command acts
in your current context: your personal account, or the organization
`latere org` selected. Run `latere login` first (see the
[main README](../README.md#sign-in)).

## Definitions

An experiment is built from definitions, each kept in versions: a suite
names its tasks, the metrics they are measured by and the primary one; a
grader produces values for a metric; a candidate is what runs, an agent, a
program in an image, or an external harness you run yourself.

```sh
latere evals suites list                    # the current version of each suite
latere evals candidates list --all          # every page, not only the first 200
latere evals tasks list -q                  # name@digest, one per line
latere evals suites show fix-flaky-tests    # one version and its fields
latere evals suites show fix-flaky-tests --versions
```

`metrics`, `graders`, `tasks`, `suites` and `candidates` each take `list`
and `show`. A list prints the current version of each name with its digest
and when it was made, and a column that says what it is: a metric's type
and level, a grader's kind and level, a task's number of checks, a suite's
tasks and primary metric, a candidate's kind and what it runs.

`show` takes a reference: a name, which reads its current version, a
`name@digest`, a digest, or an id. It prints the version's record, then its
fields as applied, in YAML. A definition names the others by digest, so a
suite's tasks read as `sha256:…`; `show` on that digest names it.

## Experiments

```sh
latere evals experiments list
latere evals experiments list --suite fix-flaky-tests --state complete
latere evals experiments show exp_01jb2c3d4e5f6g7h8j9k0m1n2p
```

`experiments list` prints each experiment newest first with the name it was
given, `-` for one started without a name, its suite by name, its number of
candidates, its state, its mode and how many of its runs are done, such as `100/100`; an
import, which declares no runs, prints the number it holds. `--suite` takes a suite's name, for every version of it, or a digest;
`--state` and `--mode` keep the experiments in that state or mode.

`experiments show` prints one experiment:

```text
id:         exp_01jb2c3d4e5f6g7h8j9k0m1n2p
name:       opus-vs-sonnet
suite:      fix-flaky-tests
candidates: opus-high, sonnet-high
reference:  opus-high
state:      running
mode:       execute
trials:     10 per cell
parallel:   4 trials at once
budget:     $25.00 cap, $3.41 spent, $21.59 left (eb-…)
started:    2026-10-20 10:00 UTC

progress
  cells:      blocked 1, complete 30, running 11
  runs:       320 of 400
  trials:     done 320, pending 80, running 20
  outcomes:   completed 311, infra_failed 9

parked cells
  CELL      TASK        CANDIDATE    STATE    REASON
  cel_01jb… flaky-gpu   sonnet-high  blocked  image_not_admitted

comparisons
  ID        COMPARES                  VARIES  CONFOUNDS  STATE      FIGURES
  cmp_01jb… sonnet-high vs opus-high  model   -          computing  provisional

console:    https://platform.latere.ai/console/evals/experiments/exp_01jb2c3d4e5f6g7h8j9k0m1n2p
```

The name is the one given when the experiment was started, which the
console asks for and an experiment created through the API keeps as the
`name` of its metadata. Definitions are named by name; two versions of one
candidate in the same experiment add their short digests, such as
`open-builder@sha256:41ab…`, and one you may not read shows its digest.
A candidate that runs an agent is named as the console names it, by the
agent, its version and its model, such as `goalless-agent v2 · haiku-5.5`;
two that would read the same add their reasoning level, and one whose
agent's versions you may not read keeps its definition's name. When the
agents API cannot be read at all, such as with a token for Evals alone,
`show` says so on stderr and names every candidate by its definition.
`runs` counts each run once, as the console's Runs tab does, so a run that
failed to start and was run again is one run. The `trials` line is the
API's own count of attempts, where such a run counts twice.

A cell is one task of one candidate, and a parked cell waits for a retry.
Each comparison says what it compares, what differs
between its sides, what else differs that its verdicts cannot separate,
whether it is computed, and whether its figures are final or may still
change as trials finish. The console's page of the experiment shows each
metric's figures and verdicts on its Results tab, and each comparison on
its Comparison tab.

## Following an experiment

```sh
latere evals experiments watch exp_01jb2c3d4e5f6g7h8j9k0m1n2p --timeout 6h
```

`watch` follows an experiment, started anywhere, until it is complete or
canceled, and prints one line per change with its time: the experiment's
state with its cells and runs done, each cell that changes state, with the
reason of one that parks, and each comparison whose figures become final.
Trials are not printed one by one. `--json` prints every frame of the
stream as one JSON line instead. A dropped connection connects again from
the last event read, so no event is shown twice or skipped.

## Exit codes

Every evals command exits with one of these, so a CI job can tell a failed
result from a refusal from an outage:

| Code | Meaning |
|------|---------|
| 0 | Done. For `watch`, the experiment completed with every cell complete. |
| 1 | A result that is a failure: the watched experiment was canceled, or completed with a parked cell. |
| 2 | Refused because of the request, such as an unknown experiment or a permission. Running the same command again gives the same answer. |
| 3 | No answer yet: the service did not answer or was busy, or `--timeout` passed. Running the command again may succeed. |

## Output and scripts

`--json` prints the API's own answers: the object for `show`, the items of
every page read for `list`. `-q` on a list prints identifiers alone, one
per line: a definition's `name@digest`, an experiment's id. A refusal
prints the API's code and sentence, then the field, reason, limit or figure
it names.

The commands call `https://api.latere.ai/v1/evals` with a token minted for
your login, for the Evals audience alone. `LATERE_EVALS_URL` or `--api-url`
names another address, and `LATERE_EVALS_TOKEN` presents a bearer as given,
such as one exchanged from a key in CI.
