---
title: "Capability-named commands: `latere environments` and `latere agents`"
status: implemented
depends_on:
  - specs/002-review-local-subcommand.md
  - specs/008-cella-at-the-origin.md
  - "CROSS-REPO: specs products/platform/capabilities/cli.md, which says the CLI teaches one platform workflow with capability-specific commands"
affects:
  - internal/commands/root.go (the command tree)
  - internal/commands/cella.go, cella_files.go, pty.go (the group becomes `environments`; every user-facing word says workload)
  - internal/commands/topos.go, topos_*.go (the group becomes `agents run` and `agents provider`; the TUI names the Latere agent)
  - internal/commands/review.go (becomes `agents review`)
  - internal/commands/retired.go (new: the retired words, refused for one release)
  - internal/commands/models.go (the help that points at the local agent)
  - cmd/latere/*_e2e_test.go (the workload set runs as `environments`)
  - docs/environments.md, docs/agents.md (renamed from docs/cella.md, docs/topos.md, docs/review.md), docs/configuration.md, docs/login-and-tokens.md, docs/models.md, README.md, CONTRIBUTING.md, CHANGELOG.md
created: 2026-10-02
updated: 2026-10-02
author: changkun
---

# Capability-named commands

The platform presents capabilities: Models, Environments, Agents, Storage,
Repositories, Apps. The console, the API paths (`/v1/models`,
`/v1/environments`) and the documentation use those names, and the CLI's own
capability statement says the CLI teaches one platform workflow with
capability-specific commands. `latere models` and `latere repos` follow it.
Three command words do not: `latere cella` and `latere topos` name the open
cores behind Environments and Agents, and `latere review`, an Agents
function, sits at the top level under no capability. A person who reads the
console's Environments section and then the CLI's help meets a second
vocabulary for the same thing.

## Why `cella` was kept until now

The command tree was renamed from `sandbox` to `cella` on 2026-04-25, when
Cella was the product a person bought. Spec 008 moved the commands onto the
Cella core at the platform origin (`https://api.latere.ai/v1/environments`)
and was scoped to the address, the credential and the commands the core
serves; it kept the word because renaming it was not its question. The
2026-09-19 platform decision then made Cella an open core and Environments
the capability the platform sells, so the word a person types follows the
capability now, as the API path already does.

## Design

### The commands

| Before | After |
|---|---|
| `latere cella <verb> ...` (alias `latere sandbox`) | `latere environments <verb> ...`, every verb kept: apply, list, get, start, stop, delete, exec, shell (alias attach), run, logs, import, export, cat, write, ls, upload, mkdir, rm, mv |
| `latere topos --local [-p P] [--dir D] [--model M]` | `latere agents run [-p P] [--dir D] [--model M]` |
| `latere topos login` | `latere agents provider` |
| `latere review [flags]` | `latere agents review [flags]` |
| `LATERE_CELLA_URL` | `LATERE_ENVIRONMENTS_URL` |
| `LATERE_CELLA_TOKEN` | `LATERE_ENVIRONMENTS_TOKEN` |
| `LATERE_TOPOS_PROVIDER_FILE` | `LATERE_AGENT_PROVIDER_FILE` |
| `latere app <verb> ...` | `latere apps <verb> ...`, every verb kept |
| `LATERE_APP_URL`, `LATERE_APP_TOKEN` | `LATERE_APPS_URL`, `LATERE_APPS_TOKEN` |

`latere agents run` is the agent on this machine, on the current directory's
files, with a local model credential. It takes no `--local` flag: running
locally is what the command is. The hosted agents run on the platform and
are reached from the console and its API, not from this command, so the
message that said the hosted platform was retired goes.

### The words a person reads

Help, examples, errors and output say workload for the object a person
creates under Environments, as the console does, and Environments for the
capability. The manifest's `kind: Sandbox` and `apiVersion:
cella.latere.ai/v1beta1` are values on the wire that a person writes into a
manifest; the help names them only where it tells a person what to write.
The human-readable list labels a record `workload:` where it said `cella:`;
`--json` output is the API's object and is unchanged. The agent's terminal
interface names the Latere agent where it said Topos.

### The retired words

`cella`, `sandbox`, `topos`, `review` and `app` stay for one release as hidden
commands. Each accepts any arguments and flags, prints one sentence naming
the replacement, and exits 1, so a script that still calls one fails on the
first run with the new command in its error rather than doing nothing. The
release after removes them, and an old word is then an unknown command.

A retired variable set while its replacement is not is refused with a
message naming the new variable. Ignoring it would send a script that
points at a staging control plane to production without a word. When both
are set the new one is used.

### Names that stay

Go file names, package aliases and identifiers keep their names: renaming
them changes nothing a person sees. The token audience `cella` is the
control plane's audience, which the issuer stamps; it is a wire value and
stays.

The local agent's provider choice moves from
`~/.config/latere/topos-provider.json` to
`~/.config/latere/agent-provider.json`, because the path is printed in
`latere agents provider --help`. A choice saved under the old name is moved
to the new one the first time it is read, so no one has to choose again.

### Out of scope

`latere eval` stays as it is. Eval is not one of the platform's
capabilities, and what its command becomes waits on that decision.

## Verification

| # | What holds | Where |
|---|---|---|
| 1 | `latere environments` carries every verb `latere cella` had, and `latere agents` carries `run`, `provider` and `review` | a command-tree test in `internal/commands` |
| 2 | `cella`, `sandbox`, `topos` and `review` are hidden, exit 1 and name their replacement, with any arguments and flags | `internal/commands` tests and the built binary |
| 3 | `LATERE_ENVIRONMENTS_URL` selects the control plane; `LATERE_CELLA_URL` alone is refused naming the new variable | `internal/commands` tests |
| 4 | No help, example, error, README or docs page names `cella`, `topos` or `latere review` as a command, except the retired-word refusals | a scan test over shipped sources and docs |
| 5 | A provider choice saved at the old path is read and moved to the new one | `internal/commands` tests |
| 6 | The built binary runs the workload commands as `latere environments` against a fake control plane | `cmd/latere` e2e tests |

## Outcome

Implemented as designed, with these additions:

- `LATERE_CELLA_TOKEN` and `LATERE_TOPOS_PROVIDER_FILE` are renamed as well,
  under the same refusal rule; the design named only the URL variable.
- `internal/commands/retired.go` holds every old word and variable, and a
  scan test over shipped sources, the README, CONTRIBUTING and `docs/` fails
  on any other mention, comments included.
- The documentation pages became `docs/environments.md` and
  `docs/agents.md`, the latter merging the local agent's page and the
  review's. Links to platform pages name `/docs/environments/` and
  `/docs/models/`, which the platform redirects the old paths to.
- The test entry points clear the retired variables, since one left in a
  developer's shell would otherwise fail unrelated tests with the refusal.
- Go file names (`cella*.go`, `topos*.go`) and identifiers keep the core's
  names; CONTRIBUTING says so.
- `latere app`, which spec 010 added and v0.16.0 released while this was
  built, broke the same rule with a singular word where every other group
  is the capability's plural. It became `latere apps`, and `app`,
  `LATERE_APP_URL` and `LATERE_APP_TOKEN` are refused like the other old
  words. Spec 010 keeps the name it shipped under.
