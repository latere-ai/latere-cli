---
title: "`latere evals`: suites, tasks, metrics, graders and candidates applied from YAML, experiments run, changed, followed, re-graded, closed and deleted, baselines and taxonomies, comparisons in the terminal and on request, trials, an external harness's lease, renewal and submit, its past trials imported with their provenance declared, reports, benchmarks and datasets published and shared by visibility and evidence level, benchmark submissions, bar checks for CI and dataset exports, on Evals at the platform origin, in Examen's build order; `latere eval` removed"
status: in-progress
depends_on:
  - specs/001-auth-unification-migration.md
  - specs/010-app-command.md
  - specs/011-capability-commands.md
  - "CROSS-REPO: latere-ai/examen specs/003-api.md: every route the group calls, the progress stream, idempotency and the errors it branches on, the external harness's lease, renewal and submit, imports of an external harness's past trials, the v0.1 profile and its build order, taxonomies, reports, publications, submissions and shares"
  - "CROSS-REPO: latere-ai/examen specs/005-execution.md: directory outputs as archives with their output manifest, an external trial's declared harness version and image, and its service versions and usage"
  - "CROSS-REPO: latere-ai/examen specs/009-bars.md: the bar check's outcomes and the exit code a command-line client maps each to"
  - "CROSS-REPO: latere-ai/examen specs/010-reports.md: the presentation rules and formats every figure obeys"
  - "CROSS-REPO: latere-ai/platform specs/214-evals-hosted.md: Evals at https://api.latere.ai/v1/evals, an experiment of external candidates created from the command line, and `sign_in_not_kept` for any other"
  - "CROSS-REPO: latere-ai/platform specs/215-evals-in-the-console.md: the prefilled create address the refusal links to"
  - "CROSS-REPO: latere-ai/platform specs/217-the-evals-api-at-the-origin.md: who may call the API at the origin, and the grants a key carries, among them an external harness's lease and submit"
  - "CROSS-REPO: latere-ai/auth deploy/base/clients.yaml: the latere-cli row's actor_audiences, which must list examen"
  - "CROSS-REPO: latere-ai/examen specs/013-publishing.md: reports, benchmarks and datasets published as numbered versions with a digest, `private`, `unlisted`, and `public` only through a listing, the evidence levels `aggregates`, `outputs` and `full` with a secret scan, share grants, withdrawal, and benchmark submissions"
  - "CROSS-REPO: latere-ai/platform specs/218-evals-results-shared.md: the Evals gallery listing request at the platform's account route, reviewed where app listings are"
  - "CROSS-REPO: latere-ai/platform specs/219-evals-end-to-end-the-goalless-study.md: the goalless study, the first user of `harness import`, of the adapter's lease, renewal and submit, and of `publish` and `share`"
affects:
  - "internal/commands/evals.go, evals_apply.go, evals_watch.go, evals_compare.go, evals_harness.go, evals_import.go, evals_upload.go, evals_publish.go (new: the group, its client, manifests, the stream, the terminal comparison, the harness, the import, the upload of an external trial's outputs, reports, publications, submissions and sharing)"
  - "internal/commands/root.go (the group added, `eval` removed)"
  - "internal/commands/exit.go (the group's exit codes)"
  - "internal/commands/retired.go (new again: `eval` and its variables refused for one release), capability_scan_test.go (`eval`, `EVAL_API_URL` and `EVAL_ADMIN_TOKEN` added to the old names)"
  - "internal/commands/eval.go and eval_*_test.go, cmd/latere/eval_*_e2e_test.go (deleted)"
  - "internal/commands/manifest_input_test.go, cmd/latere/manifest_input_e2e_test.go (the `eval` case removed)"
  - "cmd/latere/evals_e2e_test.go (new: the built binary against a stub of the API and the mint)"
  - ".lateregate.yaml (the identity block's audiences gain examen)"
  - "docs/evals.md (new), docs/configuration.md, docs/login-and-tokens.md, README.md, CHANGELOG.md"
created: 2026-10-17
updated: 2026-10-21
author: changkun
---

# `latere evals`

## Overview

Evals is the platform's ninth capability: experiments on agents and on
programs built on models, run on the Examen core at
`https://api.latere.ai/v1/evals` and reported as comparisons (platform
spec 214). The console is where a person starts and reads most
experiments (platform spec 215). The command line is where the same
person keeps a suite in files under version control and applies it,
reads a comparison in a terminal, creates an experiment their own
external harness runs and leases its trials, brings that external
harness's past trials in, publishes reports, benchmarks and datasets
for others to read, and holds a release to a bar in CI.

This spec designs the `latere evals` group and removes `latere eval`,
the command group of the retired Eval service.

## Current state

- **`latere eval` reaches nothing.** `internal/commands/eval.go` talks
  to `https://eval.latere.ai` (`EVAL_API_URL`, `--api-url`) with the
  bearer in `EVAL_ADMIN_TOKEN` or `--token`, a token for the `eval`
  audience held by a platform administrator, not the login. Its three
  commands, `apply`, `suites` and `cells`, call
  `/api/v1/suites/apply`, `/api/v1/suites` and `/api/v1/cells`. The
  Eval service, its host and its database were removed on 2026-10-17
  (`latere-ai/specs` `decisions/2026-10-17-evals-on-examen.md`), so
  every `latere eval` command now fails to connect.
- **Its tests.** Seven unit test files
  (`internal/commands/eval_test.go`, `eval_apply_response_test.go`,
  `eval_manifest_documents_test.go`, `eval_manifest_limit_test.go`,
  `eval_output_test.go`, `eval_prompt_text_test.go`,
  `eval_redirect_test.go`), six end-to-end files
  (`cmd/latere/eval_*_e2e_test.go`), and an `eval` case in
  `internal/commands/manifest_input_test.go` and
  `cmd/latere/manifest_input_e2e_test.go`.
- **Its mentions.** The README's "For operators" paragraph,
  `docs/configuration.md`'s `EVAL_API_URL` and `EVAL_ADMIN_TOKEN` rows,
  the out-of-scope paragraph of spec 011, and the note in
  `specs/README.md` that the group shipped without a spec.
- **How a product command authenticates.** `latere apps` mints one
  actor token per command for its audience (`appAudience` in
  `internal/commands/app.go`, `api.ActorToken` in
  `internal/api/actor.go`), reads `LATERE_APPS_URL` and
  `LATERE_APPS_TOKEN` from the environment (the names are constants in
  `internal/commands/capability_env.go`), and builds its issuer from the
  base URL, `AUTH_URL` or `--auth-url`.
- **No `examen` audience.** The latere-cli client lists
  `cella, arca, origo, api.latere.ai, insula` as actor audiences
  (`auth/deploy/base/clients.yaml`), and `.lateregate.yaml`'s identity
  block lists `cella, drive.latere.ai, origo, api.latere.ai, insula`.
  Platform spec 214 adds `examen` to the console's and this client's
  actor audiences at auth.
- **Exit codes.** `HandleExitError` (`internal/commands/exit.go`) passes
  a remote command's code, returns 2 for a review with unresolved
  attacks, and 1 for every other error.
- **Retired words.** Spec 011's old words `cella`, `sandbox`, `topos`,
  `review` and `app`, and its old variables, were refused for one release
  and removed in v0.32.0, together with `retired.go`, which refused them;
  each is now an unknown command or an unread variable.
  `capability_scan_test.go` still fails on any shipped mention of an old
  command or variable.
- **What Examen defines for the new commands.** Examen's specs 003,
  005 and 013 define what the commands below call and nothing on this
  side exists yet: `POST /experiments/{id}/imports` with `executor:
  external`, which takes an external harness's past trial with declared
  times, harness version and image into an experiment in mode `import`
  (a lease on such an experiment is `409 wrong_mode`, so past trials
  cannot go through lease and submit); `POST /experiments/{id}/close`;
  lease renewal; directory outputs, carried as a tar archive in the
  owner's Storage and never inline; taxonomies; reports; and
  publications, submissions, share grants and the public reads.
- **Parts to reuse.** `readManifestBodyWithLimit` (`cella.go`) reads a
  manifest from a file or standard input with a bound;
  `formatWrappedField` and `printJSON` (`format.go`) print records and
  JSON; `term.IsTerminal` tells a terminal (`app_logs.go`);
  `defaultPlatformURL` and `LATERE_PLATFORM_URL` name the console's
  origin (`repos.go`).

## Design

### Address and credential

| Setting | Value |
|---|---|
| Base URL | `https://api.latere.ai/v1/evals`; `LATERE_EVALS_URL` or `--api-url` overrides it |
| Bearer | an actor token minted at auth for the audience `examen`, one per command and renewed when a long command outlives it; `LATERE_EVALS_TOKEN` presents a bearer as given |
| Other audiences | an actor token for `arca`, to upload an external trial's outputs into the context's Storage space, and one for `api.latere.ai`, for the platform's listing route; each minted like the bearer, and both already among this client's actor audiences |
| Issuer | inferred from the base URL's host, or `AUTH_URL`, or `--auth-url` |
| Context | the saved login's, personal or an organization's, as `latere org` sets it; Examen resolves names in it |
| Console | `LATERE_PLATFORM_URL`, `https://platform.latere.ai` by default, for the links the group prints |

The login is the one `latere login` saves, and in CI the one
`latere login --token` saves; nothing new is stored. An actor token
lasts at most 300 seconds (platform spec 214), so a command that runs
longer, such as `--watch`, mints a fresh one when the stream closes at
its bearer's expiry and reconnects (The progress stream, below).

### Manifests

A manifest is YAML: one or more documents of one kind, separated by
`---`, each the kind's fields as Examen spec 002 defines them and as
Examen's own examples write them (Examen spec 009's candidate and
bar, spec 006's comparator grader). A taxonomy's document is its
`canonical` and `map` (Examen spec 003). The command names the kind, so a document carries no type
marker, and a candidate's own `kind` (`topos`, `program` or `external`)
means what Examen says it means.

```yaml
# candidates.yaml
name: research-agent-v12-opus-high
kind: topos
topos:
  agent: sha256:4e1f…        # the agent version
  model: anthropic/claude-opus-4-1
  reasoning: high
---
name: acme-harness-1-4-gpt-5-low
kind: external
external:
  harness: acme-harness
  version: "1.4.0"
  model: openai/gpt-5           # declared by the harness
  reasoning: low                # declared by the harness
  flags: {sandbox: "off"}       # the harness's own settings, each a string
```

**Names become pins.** Examen refuses a reference by name alone in a
request body (`422 unpinned_reference`). A manifest may still name
another Examen object by name, and the command pins it before it sends
anything: to the digest an earlier document of the same command was
applied as, else to the current version `GET /{kind}/{name}` answers.
It prints each pin it made, `pinned task fix-flaky-test@sha256:0b3e1c2d4f5a`,
so the person sees what the stored object names. A `name@digest`, a
digest and a built-in `builtin:<name>` are sent as written. The fields
it pins are a suite's `tasks`, `metrics`, `graders`, `canonicalizers`,
`primary_metric`, `decline_judge` and the keys of `weights`, a
cell-level metric's `of`, a task's `checks`, a grader's `produces`, and
a bar rule's `metric`. References into other
capabilities (an agent version, a model id, an image, a commit, a
Storage path) are sent as written, and Examen refuses one that is not
pinned.

**Local text.** A task's `instruction` and an inline input's `text` may
be `file://<path>`, read relative to the manifest (the current
directory for standard input) and inlined, as `latere eval` did for
prompts. The group reads a manifest file with a guard of its own on its
size, one constant in `evals_apply.go`; it holds no copy of Examen's
limits, and `413 request_too_large` or `422 limit_exceeded` from the
server is printed with the constant `details.constant` names.

**A directory.** `latere evals apply -f <dir>` applies the files
`metrics.yaml`, `graders.yaml`, `tasks.yaml`, `suites.yaml`,
`candidates.yaml` and `bars.yaml` (or `.yml`) it finds, in that order,
so every pin of a later file resolves against an earlier one. Any other
file in the directory refuses the command before a request, naming it.

### Commands

`<ref>` is an Examen reference: a name, `name@digest`, a digest or an
id (Examen spec 003, References).

| Command | Routes | Behavior |
|---|---|---|
| `apply -f <dir>` | the kinds' `PUT`s, in order | applies a directory of manifests (above) |
| `metrics apply -f <file>` (also `graders`, `tasks`, `suites`, `candidates`) | `PUT /{kind}/{name}` per document | prints each object's name, digest and `created` or `unchanged` (`201` or `200`); a `412` names the object |
| `<kind> list [--all]` | `GET /{kind}` with the kind's filters as flags | the current version of each name: name, digest, updated, and per kind its key column (a suite's task count and primary metric, a candidate's kind and what it runs) |
| `<kind> show <ref> [--versions]` | `GET /{kind}/{ref}`, `GET /{kind}/{name}/versions` | the version's fields as a record; with `--versions`, every version, the current one marked |
| `taxonomies apply -f <file> --suite <name> --metric <name>` | `PUT /suites/{suite}/taxonomies/{metric}` | appends the open-vocabulary metric's next taxonomy version from a document of `canonical` and `map`, and prints its version and `created` or `unchanged` |
| `taxonomies show --suite <name> --metric <name> [--history]`, `taxonomies unmapped --suite <name> --metric <name>` | `GET /suites/{suite}/taxonomies/{metric}`, its `/history` and its `/unmapped` | the current record, or every record; the raw labels the current record does not map, each with its count, most frequent first |
| `experiments run [-f experiment.yaml] [flags] [--watch]` | `POST /experiments` | creates an experiment (below) |
| `experiments list [--suite] [--state] [--mode] [--all]` | `GET /experiments` | id, suite, candidates, state, mode, trials done of declared, started |
| `experiments show <id>` | `GET /experiments/{id}`, `GET /experiments/{id}/cells?state=…` | the experiment, its progress, its budget's cap, spend and remainder as the answer carries them, its parked cells with their reasons, and the console link |
| `experiments update <id> [--trials <n>] [--parallelism <n>] [--reference <candidate>\|baseline:<id>]` | `PATCH /experiments/{id}` | changes N while every cell is pending, the parallelism until the experiment ends, or the reference at any time, and prints the experiment |
| `experiments watch <id>` | `GET /experiments/{id}/stream` | follows an experiment started anywhere, the console included, to its end |
| `experiments cancel <id> [--yes]` | `POST /experiments/{id}/cancel` | asks first on a terminal, refuses without one unless `--yes`; says that every trial so far is kept |
| `experiments close <id>` | `POST /experiments/{id}/close` | ends the intake of an experiment in mode `import`, whose cells then move on to grading with the trials they hold |
| `experiments retry <cell>`, `experiments retry --experiment <id> --parked` | `POST /cells/{id}/retry` | retries one parked cell, or every cell the experiment has parked `blocked` or `over_budget`, and prints each cell's new state |
| `experiments regrade <id> --grader <ref> [--cell <id>]... [--trial <id>]... [--candidate <ref>]...` | `POST /experiments/{id}/regrade` | grades the named trials, or every finished trial, again with one grader version, or a candidate-level grader's candidates, and prints how many gradings it started |
| `experiments delete <id> --evidence keep\|trash\|purge [--baselines refuse\|delete] [--yes]` | `DELETE /experiments/{id}` | deletes a finished experiment; asks first on a terminal, refuses without one unless `--yes`; `--evidence` has no default |
| `baselines pin <name> --suite <name> --experiment <id> --candidate <ref>` | `PUT /suites/{suite}/baselines/{name}` | pins the candidate's cells of a complete experiment, and prints the record and `created` or `unchanged` |
| `baselines list --suite <name>`, `baselines show <name> --suite <name> [--history]` | `GET /suites/{suite}/baselines`, `GET /suites/{suite}/baselines/{name}` and its `/history` | each baseline's current record; one baseline's record, or every record of it |
| `compare <experiment> [--candidate] [--comparison] [--metric] [--tasks] [--verdict] [--wait]` | `GET /experiments/{id}/comparisons`, `GET /comparisons/{id}`, `GET /comparisons/{id}/rows` | the comparison in the terminal (below) |
| `compare <experiment> --candidate <ref> --against <candidate>\|baseline:<id>`, `compare <experiment> --candidate <ref> --task <name> --reference-task <name>` | `POST /comparisons`, then the reads above | asks for a comparison on request, against another candidate or a baseline, or across two tasks of the candidate (`variable: task`), waits until it is computed, and prints it as above |
| `trials list <experiment> [--task] [--candidate] [--state] [--outcome] [--imported] [--metric <name> --value <v\|missing>] [--retried] [--all]` | `GET /experiments/{id}/trials` | trial, task, candidate, index, outcome, one column per metric's current value, duration, cost |
| `trials show <trial>` | `GET /trials/{id}`, `GET /trials/{id}/measurements` | the outcome, times, each check run, each judgment, every current value with its rationale, the configured, sent and observed layers unmerged, what an external harness declared, the outputs summary, the evidence with its Storage paths, and the console link |
| `harness lease`, `harness lease --renew`, `harness submit` | `POST /experiments/{id}/work/lease`, `POST /trials/{id}/submit` | below |
| `harness import --experiment <id> -f <trials.jsonl> [--plan]` | `POST /experiments/{id}/imports` with `executor: external` | submits an external harness's past trials with their provenance declared (Importing past trials, below) |
| `reports create <experiment> [--comparison <id>]...`, `reports list <experiment>`, `reports show <report>` | `POST /experiments/{id}/reports`, `GET /experiments/{id}/reports`, `GET /reports/{id}` | freezes a report of the named final comparisons, every final automatic comparison when none is named, and prints its id; lists and shows reports |
| `publish report <report>\|--experiment <id>`, `publish benchmark <suite>`, `publish dataset`, each with `[--name] [--title] [--description]` on a first version or `--to <publication>` for a new one, and `[--evidence] [--exclude <path>]... [--visibility] [--wait]` | `POST /publications`, `POST /publications/{id}/versions`, `GET /publications/{id}/versions/{n}` | publishes an immutable version and prints its publication's id, the version's number, state and digest, and, when unlisted, the link (Publishing and sharing, below) |
| `publish results <benchmark> [--version <n>]` | `GET /publications/{id}/results` | the benchmark version's results table as Examen answers it: entries in order with their rank intervals and their comparison with the reference entry |
| `publish list [--kind] [--all]`, `publish show <publication> [--versions]` | `GET /publications`, `GET /publications/{id}` | each publication's kind, name, visibility, latest version and link; with `--versions`, every version and its state |
| `submissions create <benchmark key> --report <publication>[@<n>] --candidate <ref>`, `submissions list [--benchmark <publication>] [--state] [--all]`, `submissions show <id>`, `submissions withdraw <id>`, `submissions accept <id>`, `submissions reject <id>` | `POST /submissions`; `GET /submissions` or, with `--benchmark`, `GET /publications/{id}/submissions`; `GET /submissions/{id}`; `POST /submissions/{id}/withdraw`, `/accept`, `/reject` | enters one candidate of a report version in a benchmark's results table and prints `accepted` or `pending`; lists the caller's submissions, or a benchmark owner's review queue; withdraws one's own; accepts or rejects one submitted to one's own benchmark |
| `share <publication> [--visibility] [--invite <owner>]... [--revoke <owner>]... [--replace-link] [--request-listing --line <text> [--note <text>]] [--withdraw [--version <n>] [--yes]]` | `PATCH /publications/{id}`, `PUT` and `DELETE /publications/{id}/shares/{grantee}`, `POST /publications/{id}/link`, `POST /publications/{id}/versions/{n}/withdraw`, and the platform's listing route | changes who can read a publication (below) |
| `bars apply -f <file> --suite <name>`, `bars list --suite <name>` | `PUT /suites/{suite}/bars/{name}`, `GET /suites/{suite}/bars` | applies and lists a suite's bars |
| `bars check --suite <name> --bar <ref> --experiment <id> --candidate <ref> [--comparison] [--wait] [--meta k=v]...` | `POST /suites/{suite}/bars/{ref}/evaluate`, `GET /evaluations/{id}` | the release check (below) |
| `exports create --experiment <id>... [--outcome]... [--include-imported] [--all-generations] [--evidence] [--wait]`, `exports list`, `exports show <id>` | `POST /exports`, `GET /exports`, `GET /exports/{id}` | starts a dataset export and prints its id, state and Storage path; `--wait` reads it until `complete` or `failed` |

A list reads its first page unless `--all` walks every page. A name in a
flag (`--candidate`, `--suite`) is resolved to its current version's
digest before the request, as a manifest's names are.

### What each release serves

The group follows Examen's build order (its `specs/README.md`): a
command ships in the release of the command line that follows the
Examen release serving its route, and the group's tests hold each
command to a route of the pinned release's document. Evals' first
release is Examen's v0.1 profile (Examen spec 003): external candidates,
experiments in mode `execute` and `import`, lease, renewal and submit,
the import of past trials, the measurements an external harness
declares, the grid, comparisons and the progress stream. Its commands
are `apply` and each kind's `apply`, `list` and `show`; `experiments
run`, `list`, `show`, `update`, `watch`, `cancel`, `close`, `retry` and
`delete`; `compare`, on request too; `trials`; and `harness lease`,
`harness lease --renew`, `harness submit` and `harness import`.
`baselines` follow with the console section, `experiments regrade` with
checks and judges, and `taxonomies`, `bars`, `exports`, `reports`,
`publish`, `share` and `submissions` with the later steps that serve
their routes. Benchmark submissions and the results table's rank
intervals are served after v0.1.

### Running an experiment

`experiments run` takes a manifest, flags, or both, with flags winning:

| Flag | Manifest field | Sent as |
|---|---|---|
| `--suite <ref>` | `suite` | the suite pinned |
| `--candidate <ref>`, repeated | `candidates` | the candidates pinned, in order; the first is the reference unless `--reference` names another |
| `--trials <n>` | `trials` | `trials`; absent, Examen's default |
| `--spending-cap <usd>` | `spending_cap` | `budget.cap_usd_micro`, the spending cap in whole micro-dollars (Examen spec 003); required, since Examen requires a budget and a client of the platform names no Budget, which the platform creates (platform spec 214) |
| `--reference <candidate>` or `--reference baseline:<id>` | `reference` | `reference` |
| `--parallelism <n>` | `parallelism` | `parallelism` |
| `--mode import` | `mode` | `mode`: an experiment that runs no trial and takes its past trials from `harness import`; absent, `execute` |
| `--benchmark <key>` | `benchmark` | `benchmark`: the link token or public id of a published benchmark whose suite the experiment runs (Examen spec 013) |
| `--meta k=v`, repeated | `metadata` | `metadata` |
| `--idempotency-key <key>` | none | the `Idempotency-Key` header; absent, one random key per invocation, reused by the command's own retries |

A CI job that may retry passes a stable key, such as its commit, so a
retried job reads the first experiment back instead of starting a
second (Examen spec 003, Idempotency).

**Which experiments the command line creates.** An experiment whose
candidates are all `external` and whose suite has no `check`,
`model_judge` or `agent_judge` grader is created from the command line:
no person's token is ever minted for it, so the platform needs no
sign-in it keeps (platform spec 214, row 13, a reversible choice taken
on 2026-10-18 and flagged for the owner to confirm). That is an
experiment an external harness works through lease and submit, and an
experiment of past trials in mode `import`, the whole of Evals' first
release.

**`sign_in_not_kept`.** Every other experiment needs a sign-in the
platform keeps for its unattended work, which it keeps only when the
console starts the experiment, so its create from the command line is
refused `403 sign_in_not_kept` and nothing is stored. The command sends
the create all the same, so it works unchanged on the day a
kept-sign-in path exists for the command line. It then prints:

```
This experiment starts from the console for now. The platform keeps
your sign-in for an experiment only when you start it there, so its
agents, checks and judges can run after this command ends.

Finish in the console:
https://platform.latere.ai/console/evals/experiments/new?suite=fix-flaky-tests%40sha256%3A9f2c…&candidate=opus-high%40sha256%3A41ab…&candidate=sonnet-high%40sha256%3A77d0…&trials=10&spending_cap=25&from=cli
```

The link is `LATERE_PLATFORM_URL` with the prefilled create address of
platform spec 215, carrying every choice the command was given, pinned,
`mode` and `benchmark` among them.
The console fills its form from it and sends nothing until the person
presses Start. The command exits 2.

So on the hosted platform a release job that compares agents follows
an experiment the console started: `experiments watch <id>`, then
`bars check`. An external harness's job creates its own experiment of
external candidates, or works one created anywhere, and leases and
submits its trials (below).

### The progress stream

`--watch` and `experiments watch` read `GET /experiments/{id}/stream`
until its `end` frame.

- On a terminal, a short block redrawn in place: the suite, the
  experiment's id and state, the live state; trials done of declared
  and cells by state, from the experiment's `progress`; one line per
  automatic comparison with the primary metric's verdict, delta and
  interval, marked `provisional` until final; and one line per parked
  cell with its reason and the `experiments retry` command for it.
- Off a terminal, one line per change of the experiment's state, per
  cell that changes state or parks, and per comparison that becomes
  final, each with its time. Trials are not printed one by one.
- With `--json`, every frame as one JSON line, as the stream sent it.
- At `end`, the comparison of each candidate as `compare` prints its
  headline, and the console link to the experiment.

The stream closes at its bearer's expiry (Examen spec 003); the command
mints a fresh token and reconnects with `Last-Event-ID`, and a dropped
connection reconnects the same way with backoff, so no event is shown
twice or skipped. `--timeout <duration>` bounds the wait.

### The comparison in the terminal

`compare` reads Examen's computed comparison and formats it by Examen
spec 010's rules and formats. It computes no figure.

```
fix-flaky-tests · exp_01jb… · complete
sonnet-high vs opus-high (reference) · varies: model

tests_pass (primary)   improved   +7.0 pp [3.0, 11.0]
  sonnet-high   81.0% weighted over 42 tasks; 680/840 trials   [78.0%, 84.0%]
  opus-high     74.0% weighted over 42 tasks; 615/836 trials   [71.0%, 77.0%]
  why: p < 0.001 for the primary metric, tested alone without correction
  More tasks would narrow this interval more than more trials per task would.

other results
  cost_usd      regressed   +$0.0412 [0.0110, 0.0721]   q 0.012

tasks
  tests_pass    5 improved · 1 regressed · 30 no clear difference · 6 insufficient

https://platform.latere.ai/console/evals/comparisons/cmp_01jb…
```

- Without `--candidate`, every automatic comparison of the experiment,
  in candidate order; `--comparison <id>` reads one comparison.
- The primary metric's suite-level result leads whatever its verdict.
  `within_noise` reads "no clear difference" with its delta's interval
  and both sides' n; `insufficient` reads the word and its reason and no
  delta. Every verdict carries its reason.
- Every rate carries its numerator and denominator, and its missing
  values when there are any; no bare percentage is printed.
- A comparison that is not single-variable prints "not headlined: also
  differs in tools, environment" in place of its headline; a result
  with missing values prints "not a finding" with the trials without a
  value per side. Every confound is printed in its result's line with
  its tasks and request counts.
- A comparison that is not final is marked `provisional` in its title
  line. `--wait <duration>` waits for the final one.
- `--tasks` adds the verdict table of each metric (or of `--metric`):
  task, candidate and reference summaries with intervals and
  denominators, delta and its interval, p, q, verdict, reason,
  confounds, the suite row first. `--verdict` narrows the rows.
- `--json` prints an array of `{"comparison": …, "rows": […]}`, each the
  API's answers, `rows` present with `--tasks`.

### The external harness

An external candidate's trials run on the owner's own external harness
(Examen specs 003 and 005). Two commands let a script run them:

- **`harness lease --experiment <id> [--candidate <ref>] [--max <n>] [--inputs <dir>]`**
  leases up to `n` pending trials of the experiment's external
  candidates (`POST /experiments/{id}/work/lease`). For each trial it
  prints the id, the task, the lease's end, and the label the external
  harness puts on its model requests, `Lux-Labels: examen.trial=<trial>`.
  Examen finds a trial's model calls, usage and cost by the request ids
  a submit names, and by that label once Lux filters its request log by
  a request's label (platform spec 214, decision 11), so an external
  harness passes its request ids at submit and sets the label as well.
  With `--inputs`, it downloads each trial's inputs, when its task has
  any, from the addresses the
  lease answers to `<dir>/<trial>/inputs/<name>` and writes the
  instruction to `<dir>/<trial>/instruction.txt`, the trial as the lease
  answered it, with the candidate it was leased for, to
  `<dir>/<trial>/trial.json`, and the lease's id and end to
  `<dir>/<trial>/lease.json`. The route leases for one candidate at a
  time; without `--candidate`, the command asks for each external
  candidate of the experiment in order until it holds `n` trials. With
  no trial waiting it prints "No trials waiting." and exits 0.
- **`harness lease --experiment <id> --renew <lease>`** renews a lease
  the caller holds (the same route with `renew`), so a trial that runs
  longer than `EXTERNAL_LEASE` keeps it, and prints the new end. An
  external harness renews well before the end, at half the lease, say.
  A lease that already lapsed prints Examen's sentence and exits 2; its
  trials went back to pending, and their results can no longer be
  submitted.
- **`harness submit <trial> --lease <lease> --outcome <outcome> --started-at <time> --agent-ended-at <time> [--failure-code <code> --failure-detail <text>] [--outputs <dir>] [--transcript <file>] [--final-message <file>] [--lux-request <id>]... [--service-version <name>=<version>]... [--service-usage <file>] [--harness-version <version>] [--image <tag or digest>] [--measurements <file>] [--ref <text>]`**
  sends the trial's result (`POST /trials/{id}/submit`): the lease it is
  held under, one of the outcomes the route accepts, with the failure
  code and detail the route requires for `infra_failed` and
  `over_budget`, the start and end of the system under test's work in
  RFC 3339 (Examen sets the trial's own end after its checks), and every
  output the task names under `--outputs` (Outputs, below). A file
  under `--outputs` that the task does not name refuses the command
  before anything is sent, naming the path. `--transcript` and
  `--final-message` send what judges read, which Examen takes only once
  Evals keeps files (below); `--lux-request` the Lux
  request ids of its model calls; `--service-version` and
  `--service-usage` the versions of the services the run called and
  the usage each reported for itself, as Examen's `service_versions`
  and `service_usage` (a JSON file from a service's name to
  `{cost_usd_micro, units}`); `--harness-version` and `--image` the
  trial's `declared` harness version, which must equal the candidate's
  `version`, and the image it ran in, kept as declared;
  `--measurements` values the external harness computed itself in the
  check output form (`{"metrics": {...}}`), which the command sends with
  each metric name pinned to the suite's metric and Examen stores as
  self-reported, marked and never headlined; `--ref` the external
  harness's own name for the trial. A lease that expired, or one this caller does
  not hold, prints Examen's sentence ("Lease it again.") and exits 2.

**Outputs.** Every output goes into the context's Storage space, the
experiment owner's, with the command's own actor token for `arca`, and
is sent by path and digest as `{path, arca: {path, digest}}`, never
inline: a file output as the file itself, at
`files/evals-uploads/<experiment id>/<trial id>/<output>`, and a
directory output, an entry the task names ending in `/` such as
`workspace/`, as one gzip-compressed tar archive of regular files and
directories at `files/evals-uploads/<experiment id>/<trial id>/<output>.tar.gz`,
both outside the reserved `files/evals/`. One rule serves every
release. Evals' first release runs without a file store of Examen's
own (Examen spec 003, the v0.1 profile): it records each output's path
and digest without reading them, and refuses an inline output, a
transcript and a final message `422 core_not_configured`, so an
external harness leaves `--transcript` and `--final-message` out until
Evals keeps files. Once it does, Examen reads each output as the owner, checks the digest,
and keeps the trial's own copy and output manifest under its limits, so
a workspace larger than a request body still arrives whole, and the
command holds no copy of those limits.

With `-q`, `lease` prints only trial ids, one per line, so a script can
loop without parsing:

```sh
exp=exp_01jb…
while trial="$(latere evals harness lease --experiment "$exp" --max 1 --inputs work -q)" && [ -n "$trial" ]; do
  lease="$(jq -r .lease "work/$trial/lease.json")" failure=()
  start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  if ./my-harness --dir "work/$trial" --label "examen.trial=$trial"; then outcome=completed
  else outcome=infra_failed; failure=(--failure-code harness_exit --failure-detail "my-harness exited $?"); fi
  latere evals harness submit "$trial" --lease "$lease" --outcome "$outcome" "${failure[@]}" \
    --outputs "work/$trial/outputs" --started-at "$start" --agent-ended-at "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
done
```

#### The goalless study's adapter

The goalless study (platform spec 219) is the first external harness
built on these commands, and shows the whole loop. Its `experiment.sh --lease
<experiment id> --jobs <n>` starts `n` workers, and each one, for each
candidate of the study's `evals/candidates.yaml` in turn:

1. leases one trial of that candidate with `harness lease --experiment
   <id> --candidate <name> --max 1 --inputs work -q`, and moves on to
   the next candidate on "No trials waiting.";
2. renews the lease from `work/<trial>/lease.json` in the background at
   half of `EXTERNAL_LEASE` while the run goes, since a run of the study
   has taken 735 seconds;
3. runs the study's own `run.sh` with the candidate's backend, model,
   effort and fast mode, the instruction from
   `work/<trial>/instruction.txt` and the workspace
   `work/<trial>/outputs/workspace`, from which it leaves out the
   directories the study's line count skips, behind a forwarder that adds
   `Lux-Labels: examen.trial=<trial>` to every model request and writes
   the request id Lux answers on each response;
4. classifies the outcome from the run's exit code and files, the same
   way its importer classifies a past trial; and
5. submits with `harness submit <trial> --lease … --outcome …
   --started-at … --agent-ended-at … --outputs work/<trial>/outputs
   --harness-version … --image … --lux-request …` (one per id the
   forwarder wrote) `--ref <its results path>`, and, once Evals keeps
   files, `--transcript … --final-message …`, the workspace going as one
   archive in the person's Storage (Outputs, above).

Its past trials take the other path, `harness import`, below.

The external harness signs in as any script does: the saved login,
`latere login --token` in CI, or a key whose grants name the actions it
uses (platform spec 217), presented through `LATERE_EVALS_TOKEN`. It
creates its own experiment of external candidates with `experiments
run`, or works one created in the console. It calls
models with a key of its own, the person's model key (`latere models`)
or a service account's, so those calls spend on that key's budgets and
not on the experiment's (platform spec 214).

### Importing past trials

An external harness that ran trials before Evals existed brings them in
as an experiment of declared trials: an experiment in mode `import`
whose candidates are external, created with `experiments run --mode
import`, or in the console when its suite has a check or a judge
(Which experiments the command line creates, above).

**The trial file.** `harness import` reads JSON lines, one past trial
per line, which the external harness's owner writes from their own
records; the command knows no harness's layout. Each line holds:

| Member | Meaning |
|---|---|
| `task`, `candidate` | references the command pins as a manifest's names are pinned |
| `outcome`, `failure` | as `harness submit` sends them |
| `started_at`, `agent_ended_at`, `ended_at` | the trial's declared times, in the past, each no later than the next; `ended_at` is sent as `agent_ended_at` when absent |
| `harness_version`, `image` | the declared harness version, which must equal the candidate's `version`, and the image tag or digest, sent as the import's `declared` |
| `outputs` | a directory, relative to the file, whose contents are the trial's outputs as `--outputs` sends them |
| `transcript`, `final_message` | files, relative to the file; absent, none is sent; left out until Evals keeps files (Outputs, above) |
| `lux_requests` | the trial's Lux request ids, when it recorded any; with none Lux can attribute, the trial's cost is `unknown`, never zero (Examen spec 005) |
| `service_versions`, `service_usage` | as `--service-version` and `--service-usage` send them |
| `measurements` | values the external harness recorded itself, in the check output form, with each metric name pinned, stored as self-reported |
| `ref` | the external harness's own name for the trial, such as `results20/claude/run-08`; required, since it is how a repeated import finds a trial already imported |

**The command.** `harness import --experiment <id> -f <trials.jsonl>`
sends each line to `POST /experiments/{id}/imports` with `executor:
external` (Examen spec 003), with its outputs sent as a submit sends
them, several lines at once up to
a fixed concurrency, one constant in `evals_import.go`. A line the
experiment already holds (`409 already_imported`) counts as imported,
so an interrupted import is run again unchanged and resumes. A line
Examen refuses (`422 provenance_mismatch`, `409 cell_full`, a
`not_in_experiment` reference) is reported and the import goes on. It
then prints one table of imported, already imported and refused lines
per task and candidate, and every refused line with its `ref`, code and
the members of `details` a person can act on; `--json` prints one line
per trial with Examen's answer. It never closes the experiment's intake:
the owner closes it when every past trial is in, with `experiments close` or
in the console.

**`--plan`** reads every line, resolves every pin, checks that each
file it names exists and that each outcome has the members it needs,
and prints the same table with nothing sent. It computes no digest of
Examen's (decision 12 holds), so it cannot say what Examen will refuse
for provenance, only what the file gets wrong.

**Exit codes.** 0 when every line is imported or was already; 1 when
the import ran and Examen refused some lines; 2 when the file or a
flag is invalid, before anything is sent; 3 as the table says.

### Publishing and sharing

Three things can be published, each as an immutable version (Examen
spec 013): a **report** of an experiment, a **benchmark** (a suite
others can see, run and submit results to), and a **dataset** (a
complete dataset export of experiments' trials, measurements and
evidence, with a license and a citation). A first version creates the
publication, with `--name` (unique per kind), `--title` (one line) and
an optional `--description`; `--to <publication>` publishes the next
version of one that exists. Versions are numbered from 1, a link
resolves to the newest published version unless it pins a number, and a
published version is never edited, only withdrawn. Who can read one is
its visibility: `private`, the default, for the owner and the owners it
invites; `unlisted`, for anyone with the link, signed out; and
`public`, listed in the Evals gallery, which no command sets.

- **`publish report <report> [--evidence aggregates|outputs|full] [--visibility private|unlisted]`**
  publishes a report Examen froze (Examen spec 010), or with
  `--experiment <id>` in place of `<report>` first freezes a report of
  the experiment's final automatic comparisons, as `reports create`
  does.
  `--evidence` is what readers get beside the figures, verdicts and
  confounds: `aggregates`, the default, no trial content; `outputs`,
  also each trial's outputs, workspace files, check output and current
  values; `full`, also transcripts, final messages, request records and
  judge rationales.
- **`publish benchmark <suite> [--submissions owner_only|reviewed|open] [--visibility]`**
  publishes a suite version as a benchmark, always at `aggregates`;
  `owner_only` is the default. Its primary metric must be of level
  `trial` or `cell` and not a label, or Examen refuses it.
- **`submissions create <benchmark key> --report <publication>[@<n>] --candidate <ref>`**
  enters one candidate of a report version, the newest published one
  without `@<n>`, in a benchmark version's results table; benchmark
  submissions are served after v0.1. The key is
  the benchmark's link token or public id. The report's experiment must
  have run that exact suite version, and for a benchmark anyone with the
  link can open, the report must be `unlisted` too. It prints `accepted`
  or `pending`, the latter for a `reviewed` benchmark of another owner.
  **`publish results <benchmark>`**, served with them after v0.1,
  prints the results table as Examen answers it: the entries in its order, each with its primary metric and
  interval, its rank interval as `rank 2 to 4` (`rank 1` when the two
  ends meet), never a place number alone, and its difference from the
  reference entry with its interval, verdict and reason, the reference
  entry's row marked `reference`; then the entries without enough
  trials or tasks to rank, each with why; and a last line naming
  Examen's `method`: the test, the adjustment, the level, the
  resamples and the seed. The rest of the **`submissions`** group lists
  the caller's submissions, or with `--benchmark` the review queue of a
  benchmark the caller owns, and withdraws, accepts or rejects one.
- **`publish dataset --license <spdx> [--citation <CITATION.cff>] (--export <id> | --experiment <id>...) [--evidence]`**
  publishes a dataset. `--export` names a `complete` dataset export;
  `--experiment` starts one of those experiments (`POST /exports`),
  waits for it to complete, and publishes it. There is no version flag:
  the version is the publication version's number. `--license` is
  required and must be an SPDX identifier or expression. From the
  Citation File Format file the command reads and sends the fields the
  owner gives, `authors`, `abstract`, `keywords`, `repository-code` and
  `message`; Examen fills the title, version, release date, license and
  address.
- **The secret scan.** An `outputs` or `full` version is scanned before
  it is published. `--wait` reads the version until it is `published`,
  `blocked` or `failed`; a blocked version prints each finding's path,
  line and rule, never the matched text, and exits 1. Publishing again
  with `--to` and `--exclude <path>` for each finding's path makes a
  new version that leaves those files out; `--exclude` takes an
  evidence path of the version's folder as the scan names it, not a
  pattern.
- **`share <publication>`** changes who can read it: `--visibility
  private|unlisted` (`PATCH /publications/{id}`); `--invite <owner>`
  and `--revoke <owner>`, Examen's share grants to a person or an
  organization as `latere org` names them (`PUT` and `DELETE
  /publications/{id}/shares/{grantee}`); `--replace-link`, which mints a
  new link token and stops the old one; and `--request-listing --line
  <text> [--note <text>]`, which asks the platform for an Evals gallery
  listing at its account route, `PUT
  /v1/account/evals/publications/{id}/listing` (platform spec 218),
  since Examen's own listing route answers only a listing the platform
  approved; an administrator reviews it where app listings are
  reviewed. A listing that ends returns the item to `unlisted`. An
  organization whose administrator does not allow its members unlisted
  items refuses `--visibility unlisted` with the platform's code.
- **`share <publication> --withdraw [--version <n>]`** withdraws one
  version, or every published version without `--version`: a link that
  pins it answers that it was withdrawn, the publication's link
  resolves its newest version still published, or answers withdrawn
  when none is, and its evidence links are revoked. On a terminal it
  asks first; off one it needs `--yes`.
- **`publish list`** and **`publish show <publication> [--versions]`**
  print each publication's kind, name, visibility, latest version, state
  and link, and with `--versions` every version.

Neither command takes `public`: `--visibility public` refuses the
command before anything is sent and prints "Public items are listed in
the Evals gallery after review. Use --request-listing.", and exits 2.

**`sign_in_not_kept` for a publish.** On the hosted platform a publish
that copies files (a report at `outputs` or `full`, any dataset), any
benchmark, whose tasks' Storage inputs Examen links, and a withdrawal
of a version with files each need a sign-in the platform keeps for that
one act, which only the console obtains (platform spec 218), so from
the command line they are refused `403 sign_in_not_kept` and nothing is
stored. The command sends the request all the same, as `experiments
run` does, so it works unchanged once the command line has a
kept-sign-in path; it prints the sentence and the console's address for
the same act, the Publish page with its source (platform spec 215,
`/console/evals/published/new?report=…`, `?benchmark=…` or
`?dataset=…`) or the publication's page for a withdrawal, and exits 2.
A report at `aggregates`, `submissions create`, and `share --visibility`,
`--invite`, `--revoke`, `--replace-link` and `--request-listing` work
from the command line.

The goalless study publishes this way: each experiment's report
`unlisted` at `aggregates` from the command line, and from the console,
which keeps the sign-in they need, the suite as a benchmark with
submissions reviewed and a listing request, and the study as the
dataset `goalless-agents` under `MIT`, with the repository's
`CITATION.cff` and evidence `outputs`; its workspaces were imported
without their vendored directories, so nothing needs excluding
(platform spec 219).

### The release check

Examen ships no command line (Examen spec 009): its bar check is the
evaluation call and a table of outcomes, each with the exit code a
command-line client maps it to. `bars check` defines its own flags,
maps that table to its exit codes, and so holds a CI job to the same
outcomes any client reads. It resolves the candidate's name
to its current digest, evaluates the bar, and with `--wait <duration>`
reads the experiment and the evaluation again at a fixed pace, one
constant in `evals.go`, until the evaluation is complete, or the
experiment ended, or the duration passed. It prints the result, one line per failed rule with
the metric, the level, the verdict, the delta with its interval and the
confounds, and the console link; `--json` prints the evaluation.

```sh
latere evals experiments watch "$EXP" --timeout 6h &&
latere evals bars check --suite topos-release --bar release \
  --experiment "$EXP" --candidate topos-release-candidate --wait 30m \
  --meta commit="$GITHUB_SHA"
```

### Output

- **Records and tables.** A record prints as `label: value` lines
  (`formatWrappedField`); a list as a table with the CLI's uppercase
  headers, as `latere apps list` prints them. Times are relative in a
  list and absolute in a record. Money, rates, differences, p and q
  values and intervals follow Examen spec 010's formats.
- **`--json`** prints the API's own answers: the object for `show`,
  `run` and `apply` (an array for several documents), the items of every
  page read for `list`, the evaluation for `bars check`, and one line
  per frame for a stream.
- **`-q`, `--quiet`** prints identifiers only, one per line: each applied
  object's `name@digest`, an experiment's id, a lease's trial ids, a
  list's names or ids.
- **Off a terminal**, nothing is redrawn and escape sequences in text
  the service returned are removed, as `latere apps logs` does.
- **Refusals** print `<code>: <message>` from Examen's error, then the
  members of `details` a person can act on (`field`, `reason`,
  `constant`, `figure`) one per line. A `401` adds the line that says to
  run `latere login`.

### Exit codes

The group returns the exit codes Examen spec 009 maps the bar check's
outcomes to, for every command, through a typed error
`HandleExitError` passes on:

| Code | Meaning |
|---|---|
| 0 | done: an apply, a read or a submit succeeded; a bar passed; a watched experiment completed with every cell complete |
| 1 | a result that is a failure: a bar failed; a watched experiment was canceled or completed with a parked cell; an export failed; an import ran and Examen refused some of its runs; a publication's version was blocked by the secret scan |
| 2 | refused because of the request: invalid flags or manifest, an unknown object, a permission, `sign_in_not_kept`, `plan_suspended`, an expired lease, any `4xx` but `408` and `429`. Running the same command again gives the same answer |
| 3 | no answer yet: the service did not answer, answered `5xx`, `408` or `429`, a stream could not be reconnected, or `--wait` or `--timeout` passed. Running it again may succeed |

### The removal of `latere eval`

- `eval.go`, its seven unit test files and its six end-to-end files are
  deleted, `newEvalCmd` leaves `root.go`, and the `eval` case leaves the
  two manifest input tests.
- `eval` becomes a retired word for one release, under spec 011's rule:
  hidden, taking any arguments, it prints "'latere eval' was the retired
  Eval service's command; use 'latere evals', whose manifests follow the
  Evals API" and exits 1. The release after removes it.
- `EVAL_API_URL` and `EVAL_ADMIN_TOKEN` join the retired variables as
  `LATERE_EVALS_URL` and `LATERE_EVALS_TOKEN`: either set without its
  replacement refuses the command, so a script that pointed the old group
  at another address never reaches production unawares. The test entry
  points clear them.
- The scan's pattern gains `latere eval\b` and
  `EVAL_(API_URL|ADMIN_TOKEN)\b`. The word boundary leaves
  `latere evals` and the shell's `eval "$(latere models env)"` (in
  `docs/models.md` and `docs/login-and-tokens.md`) unmatched, since
  neither is `latere eval` followed by a boundary.
- The README's "For operators" paragraph goes, its Capabilities table
  gains `latere evals`, `docs/evals.md` is the group's guide,
  `docs/configuration.md` lists `LATERE_EVALS_URL` and
  `LATERE_EVALS_TOKEN` in place of the old rows, and the CHANGELOG says
  what moved.

## Decisions

1. **The command names the kind; a document has no type marker.**
   *Rejected:* a `kind:` marker, which collides with a candidate's own
   `kind` as Examen's documents write it; and an envelope such as
   `{type, spec}` that no Examen document uses.
2. **Names are pinned by the command, and every pin is printed.**
   *Rejected:* manifests that hold digests only, which nobody writes by
   hand; and resolving names at the server, which Examen refuses by
   design so that a stored reference always names one version.
3. **Only Examen's own objects are pinned by name.** *Rejected:*
   resolving an agent version, a model alias or an image tag through
   their capabilities, which needs audiences this client does not hold
   and would bury another capability's choice inside an apply.
4. **`experiments run` sends every create, one the platform refuses for
   want of a kept sign-in included.** *Rejected:* refusing it locally,
   which would copy the platform's rule on candidates and graders into
   the command and need a release of the command line on the day the
   platform keeps its sign-in.
5. **The refusal links to a prefilled console form.** *Rejected:* the
   error alone, which leaves the person to enter every choice again.
6. **One exit-code table for the group, Examen's release check's.**
   *Rejected:* the CLI's general 1 for every error, which a CI job
   cannot tell from a failed bar or a service that was down.
7. **A long stream renews its token and reconnects.** *Rejected:* a
   longer-lived token, which auth does not issue to this client.
8. **The terminal comparison formats Examen's figures.** *Rejected:*
   computing summaries from trials in the client, which would print
   figures Examen never stored and that a console could contradict.
9. **`--json` is the API's own bytes.** *Rejected:* a JSON shape of the
   command line's own, which a script would have to learn twice.
10. **The external harness is lease and submit, with `-q` for scripts.**
    *Rejected:* a command that runs the external harness itself, which would
    decide for the owner how a trial is executed; it can follow once
    scripts show the common shape.
11. **`latere eval` is retired for one release, and its variables
    refused.** *Rejected:* deleting the word outright, against spec
    011's rule that an old word answers with its replacement for one
    release.
12. **No dry run.** *Rejected:* computing digests locally to show what
    an apply would change, a second canonicalization that could disagree
    with Examen's.
13. **`harness import` reads a trial file the external harness's owner
    writes, not any harness's own layout.** *Rejected:* reading a study's results
    directories, which puts one harness's format into a general tool;
    and importing through lease and submit, which Examen refuses for an
    experiment in mode `import`.
14. **A repeated import resumes by `ref`.** *Rejected:* a local record
    of what was sent, which a second machine or a lost directory would
    not have, while Examen already answers `already_imported`.
15. **`--plan` checks the file, not Examen's answer.** *Rejected:*
    predicting provenance refusals locally, the second canonicalization
    decision 12 rejects.
16. **Lease renewal is a flag of `harness lease`, and an external harness
    runs it.**
    *Rejected:* renewing inside `harness submit` or a background process
    of the command line, which would decide for the owner how a trial is
    run (decision 10).
17. **No command sets `public`.** *Rejected:* a `--visibility public`
    that the server refuses, which suggests a path that does not exist;
    an item is public only through a listing an administrator approves.
18. **Publishing and sharing are two commands.** *Rejected:* one command
    with every flag, which mixes making an immutable version with
    changing who reads it, two acts with different undo: a version is
    withdrawn, never edited, while a visibility changes back.
19. **Every output goes to the context's Storage space, a directory as
    one archive, and is sent by path and digest.** Examen never takes a
    directory output inline, and its first release takes no output
    inline at all. *Rejected:* inline file outputs, which the first
    release refuses and which would make the command choose by size;
    file-by-file submission of a directory, which Examen does not
    define; and an upload into the reserved `files/evals/`, which Examen
    writes.
20. **The listing request goes to the platform, not to Examen.**
    *Rejected:* Examen's `PUT /publications/{id}/listing`, which an
    installation allows only for a listing its own review approved, so
    a person asking there is always refused.
21. **Benchmark submissions are one group, `submissions`.** *Rejected:*
    `publish submit` beside a `submissions` group, two places for one
    object; and submitting under `publish`, which makes an immutable
    version, while a submission is withdrawn and reviewed like a
    request.

## Not in this spec

- A kept-sign-in path for the command line, which is the platform's and
  auth's to design (platform spec 214).
- Importing a Topos session or a Cella sandbox (Examen's imported
  trials step), Examen's grants of a suite, a baseline or an experiment
  to another owner, a report's narrative, its drafting and linter and
  its static HTML exports (after v0.1), the annotation queue and a
  judge's calibration: later steps of Examen's build order, each a
  command of its own when it is served.
- Inviting a person by email address to a publication, which the
  console does through the platform's own route (platform spec 218);
  `share --invite` names an owner as Examen's share grant does.
- Uploading a task's input files to Storage; a manifest names a Storage
  path with its digest.
- Per-candidate progress in the watch block, which Examen's progress
  does not carry and the command will not add up.
- A command that runs an external harness.

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | Every route the group calls is a route of Examen's OpenAPI document under the base URL | `TestEvalsRoutesAreExamens` |
| 2 | The bearer is an actor token for `examen`, never the login token, unless `LATERE_EVALS_TOKEN` is set | `TestProductCredentialsCarryOnlyTheirOwnAudience`, `TestEvalsTokenFromTheEnvironment` |
| 3 | An apply pins each name to the digest an earlier document was applied as, else to the current version, prints every pin, and sends a pinned reference as written; a directory applies in kind order and refuses a file it does not know | `TestEvalsApplyPinsNames`, `TestEvalsApplyDirectory` |
| 4 | `file://` instructions and inline inputs are read relative to the manifest and inlined; a refusal for size names Examen's constant | `TestEvalsApplyInlinesFiles`, `TestEvalsApplyNamesTheLimit` |
| 5 | `experiments run` sends the choices pinned, with one `Idempotency-Key` across its own retries or the one given | `TestEvalsRunSendsPinnedChoices`, `TestEvalsRunIdempotencyKey` |
| 6 | A create of external candidates the platform answers `201` prints the experiment; a create refused `sign_in_not_kept` prints the sentence and a console link that carries every choice in platform spec 215's parameters, `spending_cap` among them, and exits 2 | `TestEvalsRunCreatesExternal`, `TestEvalsRunSignInNotKept` |
| 7 | A watch follows the stream to `end`, renews its token and reconnects with `Last-Event-ID` with no event shown twice, prints on a terminal and off one as designed, and exits 0, 1 or 3 as the table says | `TestEvalsWatch`, `TestEvalsWatchRenewsAtExpiry`, `TestEvalsWatchExitCodes` |
| 8 | The terminal comparison leads with the primary result whatever its verdict, prints every rate with its denominator and every verdict with its reason, never headlines a confounded comparison or a result with missing values, marks a provisional one, and `--json` is the API's answers | `TestEvalsCompare`, against golden outputs |
| 9 | Trial filters reach the route's parameters, and a trial prints its checks, judgments, values with rationales and the three layers unmerged | `TestEvalsTrials` |
| 10 | A lease downloads each trial's inputs and prints the label, `-q` prints ids alone, a submit sends the outcome, times, outputs, request ids and reported values, and an expired lease exits 2 | `TestEvalsHarness` |
| 11 | `bars check` exits 0, 1, 2 and 3 in the cases of the table, and `--wait` reads until the evaluation completes | `TestEvalsBarCheckExitCodes`, against a stub server and a fake clock |
| 12 | `exports create --wait` exits 0 on `complete`, 1 on `failed` and 3 when the wait passes | `TestEvalsExportWait` |
| 13 | Every exit code of the group reaches the process unchanged | `TestHandleExitErrorKeepsTheEvalsCode` |
| 14 | `eval` is a hidden word that exits 1 naming `latere evals` with any arguments; `EVAL_API_URL` or `EVAL_ADMIN_TOKEN` alone is refused naming its replacement; no shipped source or doc names the old command or variables; the old group's code and tests are gone | `TestRetiredCommands`, `TestRenamedVariables`, `TestNothingNamesARetiredCommand` |
| 15 | The built binary applies a directory, creates an experiment of external candidates, is refused a create of agent candidates and prints the link, watches a stubbed experiment to its end, compares, asks for a comparison across two tasks, pins a baseline, applies a taxonomy, checks a bar, leases, renews and submits a trial, imports a run file, closes an import experiment, freezes, publishes at `aggregates` and shares a report, and is refused a benchmark's publish `sign_in_not_kept` with the console's Publish address, against a stub of the API, the mint and Storage | `cmd/latere/evals_e2e_test.go` |
| 16 | `--inputs` writes `instruction.txt`, `trial.json` and `lease.json`; `--renew` extends a held lease and exits 2 on a lapsed one; a submit sends the lease, the failure members with `infra_failed`, the transcript, the final message, the declared harness version and image, the service versions and usage, and the ref; a file output is uploaded as itself and a directory output as one gzip-compressed tar archive under `files/evals-uploads/`, each sent as `{path, arca: {path, digest}}`, never inline; a file the task does not name is refused before sending | `TestEvalsHarnessRenew`, `TestEvalsHarnessSubmitMembers`, `TestEvalsHarnessDirectoryOutput` |
| 17 | `harness import` sends every line to the import route, counts `already_imported` as imported so a second run changes nothing, reports refused lines with their `ref` and goes on, and exits 0, 1, 2 or 3 as its section says; `--plan` sends nothing | `TestEvalsImport`, `TestEvalsImportResumes`, `TestEvalsImportPlanSendsNothing` |
| 18 | `publish` sends the kind, name, title, evidence level and visibility, or `--to` for a new version; a dataset requires an SPDX license, reads the owner's CFF fields, and names a complete export or starts one; `--exclude` sends paths as given; with `--wait` a blocked version prints each finding's path, line and rule without its matched text and exits 1; `submissions create` sends the benchmark key, the report version and the candidate and prints `accepted` or `pending` | `TestEvalsPublish`, `TestEvalsPublishBlocked`, `TestEvalsSubmit` |
| 19 | `share` changes visibility, grants and revokes shares, replaces the link, requests a listing at the platform's account route, and withdraws a version by number after asking on a terminal or with `--yes`; `--visibility public` exits 2 with nothing sent | `TestEvalsShare`, `TestEvalsNoPublicVisibility` |
| 20 | `experiments update`, `close`, `regrade` and `delete`, `baselines pin`, `taxonomies apply`, `reports create` and `compare --against` or `--task` reach their routes with the members the table names; `experiments run --mode import --benchmark <key>` sends both and carries both in the console link; `--spending-cap` is required and sent as `budget.cap_usd_micro` | `TestEvalsExperimentChanges`, `TestEvalsBaselines`, `TestEvalsTaxonomies`, `TestEvalsReports`, `TestEvalsCompareOnRequest`, `TestEvalsRunSendsPinnedChoices` |

## Outcome

In progress. The first slice shipped on 2026-10-21 against Examen v0.6.4
as the platform serves it at `https://api.latere.ai/v1/evals`.

**Built.**

- `metrics`, `graders`, `tasks`, `suites` and `candidates`, each with
  `list [--all] [--json] [-q]` and `show <ref> [--versions] [--json]`.
- `experiments list [--suite] [--state] [--mode] [--all] [--json] [-q]`,
  `experiments show <id> [--json]` and `experiments watch <id> [--timeout]
  [--json]`.
- The address and credential as designed: `LATERE_EVALS_URL` or
  `--api-url`, ending in `/v1/evals`; an actor token for `examen` minted
  from the saved login and minted again as it lapses; `LATERE_EVALS_TOKEN`
  presented as given. `.lateregate.yaml`'s identity block lists `examen`.
- The exit-code table, through `evalsExitError`, which `HandleExitError`
  passes on.
- Acceptance criteria 1 (`TestEvalsRoutesAreExamens`, against the routes of
  the served document in `internal/commands/testdata/evals-routes.txt`), 2
  and 13 are met; 7 and 15 in part (`TestEvalsWatch`,
  `TestEvalsWatchExitCodes`, `TestEvalsWatchGivesUp`,
  `cmd/latere/evals_e2e_test.go`).

**Where it differs from the design.**

- The commands send through the Latere Go SDK's request core
  (`Config.Do`, `Config.Stream`, `latere.Items`), which carries the token,
  the retries and the API's errors. sdk-go has no `evals` package, so the
  fields the commands read are declared in `internal/commands/evals.go`,
  ready to move into one.
- `experiments show` names the suite, the candidates and the parked cells'
  tasks by reading each digest's definition; one the caller may not read
  keeps its digest. It lists the cells parked `blocked` or `over_budget`
  with their reasons, and each comparison by what it compares, what
  varies, its confounds, its state and whether its figures are final. It
  prints no verdict or figure: spec 010 prints a verdict with its interval,
  its denominators and its reason, which is `compare`'s to format.
- `watch` prints the off-terminal lines on a terminal too; the redrawn
  block is not built. It ends with the experiment's state, its progress
  and the console link rather than each candidate's comparison headline,
  which needs `compare`. Each line's time is when it was printed, since a
  stream frame carries none.
- The SDK's stream connects again after a drop and gives up after a few
  connections in a row that read no frame, which a quiet experiment's
  stream does at each token expiry. `watch` connects again after such a run
  when it read a frame or lasted `evalsStreamFloor`, and exits 3 on a
  stream that keeps closing at once.

**`latere eval` removed.** `eval.go`, its seven unit test files and its
six end-to-end files are deleted, and the `eval` case left both manifest
input tests; a writer helper the eval tests defined, which three other
tests used, moved to `output_writer_test.go` as `failingWriter`. `eval` is
the one retired word in `retired.go`: hidden, taking any arguments, it
prints "'latere eval' was the command of the retired Eval service; 'latere
evals' reads your suites and experiments in Evals" and exits 1. The
sentence names what the first slice reads rather than the manifests the
design's sentence promised, since `apply` is not built. `EVAL_API_URL` and
`EVAL_ADMIN_TOKEN` set alone refuse every `latere evals` command naming
`LATERE_EVALS_URL` and `LATERE_EVALS_TOKEN`, and the test entry points
clear them. The scan's pattern gained `latere eval\b`,
`EVAL_(API_URL|ADMIN_TOKEN)\b` and `eval\.latere\.ai`. The README's "For
operators" section and the two configuration rows are gone. The release
after the one that ships this removes the word and the two variables.

**Not built.** `apply` and each kind's `apply` (the `PUT` routes are
served), `taxonomies` and `baselines` (served), `experiments run`,
`cancel`, `close` and `retry` (served), `experiments update` and `delete`
(not served at v0.6.4), `experiments regrade`, `compare` and `compare` on
request (served), `trials` (served), `harness lease`, `--renew`, `submit`
and `import` (served), `reports`, `publish`, `share`, `submissions`,
`bars` and `exports` (not served at v0.6.4).
