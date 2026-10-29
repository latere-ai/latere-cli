---
title: "`latere memory`: see, add, change, move, delete, restore, export and import what Latere remembers about you and your projects, and turn its settings on or off"
status: implemented
depends_on:
  - specs/009-repos-command.md
  - specs/011-capability-commands.md
  - "CROSS-REPO: latere-ai/specs infrastructure/memory.md: the design, its groups, the CLI's commands in section 9, and the import prompt in section 5"
  - "CROSS-REPO: platform specs/234-memory-in-storage.md, section 7: the person's routes under /v1/account/memory and their refusals"
  - "CROSS-REPO: sdk-go memory/: the client every surface manages memory through, on the main commit the CLI requires"
affects:
  - internal/commands/memory.go (new: the group and its commands)
  - internal/commands/memory_settings.go (new: the switches, read from what the client exposes)
  - internal/commands/memory_test.go, memory_settings_test.go (new)
  - internal/commands/root.go (the group registered, the root help)
  - cmd/latere/memory_e2e_test.go (new)
  - docs/memory.md (new), README.md, CHANGELOG.md
  - go.mod (sdk-go at the main commit that adds the memory package)
created: 2026-10-29
updated: 2026-10-29
author: changkun
---

# Memory from the CLI

Latere keeps what it remembers about a person as Markdown files in their
Storage: one folder for the person, You, and one per project. Every
surface manages it the same way, through platformd's routes and sdk-go's
`memory` package, and the family design names the CLI's commands. This
records them.

## Current state

| Fact | Where |
|---|---|
| `GET /v1/account/memory` answers the settings, every memory without its body, the projects by id and name, `more`, the limits with the groups in order, and the import prompt | platform `docs/account/openapi.yaml` |
| A memory is addressed by its scope, `you` or a project's id, and its id, the file name without `.md` | the same |
| A person's delete is for good; only what a conversation forgot is kept 30 days and restorable | the same, `deleteMemory` and `listForgottenMemory` |
| Import saves each line `Group: what you remember` as typed, at most `limits.max_import_lines` lines, and calls no model | the same, `importMemory` |
| The routes are platformd's, at `platform.latere.ai` as every account route the CLI calls, with a token minted for `api.latere.ai` | `internal/commands/repos.go`, `devices_records.go` |
| sdk-go's `memory` package makes every call; v0.14.0, its newest tag, does not have it | sdk-go `memory/` on main |

## Design

### Commands

| Command | SDK call | Does |
|---|---|---|
| `memory list [--project P]` | `ListMemories` | You's memories in one table by group, in the order the platform gives the groups, then a line per project with memory; with `--project`, that project's |
| `memory show <id> [--project P]` | `GetMemory` | one memory, its body included |
| `memory add <text> [--group G] [--name N] [--body B] [--project P]` | `AddMemory` | saves the text as typed, Preferences unless `--group` says otherwise |
| `memory edit <id> [--text T] [--name N] [--group G] [--body B] [--project P]` | `UpdateMemory` | changes what the flags name and nothing else |
| `memory mv <id> <you\|project> [--project P]` | `UpdateMemory` with a scope | moves a memory to You or to a project; `--project` names where it is now |
| `memory rm <id> [--project P] [--yes]` | `DeleteMemory` | deletes it for good, after a yes |
| `memory forget [--yes]` | `ForgetEverything` | deletes every memory of You and every project for good, after a typed yes |
| `memory restore [<id>] [--project P]` | `ListForgotten`, `RestoreMemory` | without an id, lists what conversations forgot in the last 30 days; with one, brings it back |
| `memory export [-o FILE] [--force]` | `ExportMemory` | saves the zip to the file the platform names, or FILE, `-` for standard output |
| `memory import [FILE\|-] [--project P] [--prompt]` | `ImportMemory`, `ListMemories` | saves another assistant's answer, a memory per line; `--prompt` prints the words to ask it |
| `memory settings [name=on\|off ...] [--yes]` | `ListMemories`, `UpdateSettings` | prints the switches, or turns the ones named on or off |

Each command but `rm`, `forget` and `export` takes `--json`, which prints
the platform's answer as it came. The group takes `--platform-url` and
`--auth-url`, and reads `LATERE_PLATFORM_URL` and `LATERE_PLATFORM_TOKEN`,
as `latere repos` does.

### A project by its name or its id

`--project` and the destination of `mv` take a project's id (`prj_…`) or
its name as `list` shows it, matched without case. An id is sent as given;
a name is read from the list's projects, and a name two projects share is
refused with both ids.

### Groups

`--group` takes a type the platform names, `preference`, `profile`,
`topic`, `area` or `person`, or the heading a person reads for it:
Preferences, Profile, Topics, Areas, People. A word that is neither is sent
as given, so the platform's refusal names the rule, and a group the
platform adds later is taken before the CLI knows its heading. `list`
orders the groups by the platform's `limits.types` and shows a group it
has no heading for by its type.

### The switches

`settings` reads the switches from the `settings` object of the list's
JSON, every boolean member in the order the platform sends it, rather
than from a fixed list, so a switch the platform adds shows before the CLI
knows its name. A switch is changed by the member of the client's
`UpdateSettingsParams` that carries its JSON name, read from the struct, so
an sdk-go that adds one is all the CLI needs to change it. A switch the
platform shows and this client cannot change is refused with that reason
and the upgrade command, and nothing is sent. The CLI keeps a label and
a sentence for the two switches it knows, Remember new things and
Include sensitive topics; another is shown by its name.

Turning Include sensitive topics off deletes every memory marked sensitive
for good, so the command asks for a yes first unless `--yes` is given, and
says how many were deleted.

### Confirmations

`rm` asks `[y/N]` on standard error, since one memory is a small loss.
`forget` asks for `yes` typed in full, as `latere devices policy --network
any` does, since it is everything. A run with no terminal answers neither,
and is refused with "nothing was deleted" and the `--yes` that skips the
question.

### Export and import

Export writes beside its destination and renames when the zip is whole,
so an export the platform stopped partway (`ErrExportIncomplete`) leaves
no file behind and an existing file is replaced only with `--force`.

Import reads the file, or standard input. At a terminal without a file it
prints the platform's import prompt and reads the pasted answer to Ctrl-D.
A text of more lines than `limits.max_import_lines` is sent as it is, and
the command says that only the first ones were read, so the person runs it
again with the rest.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| `rm` and `forget` | `rm` one memory, `forget` everything, as the family design names them | `forget <id>` for one: the design's Forget everything is the word for all of it, and a conversation's forgetting is what `restore` brings back |
| The switches | read from the JSON and changed through the client's params by their JSON names | a flag per switch: a switch the platform adds would need a CLI release to show |
| One table with a group column | a column, rows in group order | a heading per group: noisier, and harder to `grep` |
| A memory's text in a table | cut at 72 characters, whole in `show` | whole: a 300-character line breaks the table |

## Not in this spec

Dreaming's `latere memory runs` and `undo` wait for the runs themselves.
Search and reference chats needs nothing here beyond the switch, which
`settings` shows once the platform sends it.

## Acceptance criteria

| # | Criterion | Test |
|---|---|---|
| 1 | `list` shows You by group in the platform's order, the projects with memory, `more` and Remember new things off; `--project` by name or id shows one project's | `TestMemoryListByGroup`, `TestMemoryListOfAProject` |
| 2 | `add`, `show`, `edit` and `mv` send exactly the fields asked for, to the scope asked for, and print the memory | `TestMemoryAddSendsWhatWasTyped`, `TestMemoryEditSendsOnlyTheFlagsGiven`, `TestMemoryMoveBetweenScopes` |
| 3 | `rm` and `forget` send nothing without a yes; `restore` lists and restores | `TestMemoryDeleteAsksFirst`, `TestMemoryRestore` |
| 4 | `export` writes the whole zip, leaves nothing for a cut one, and keeps an existing file; `import` sends the text and says when lines were left out | `TestMemoryExport`, `TestMemoryImport` |
| 5 | `settings` shows a switch the platform adds, changes a switch by the client's params, refuses one the client cannot change, and asks before deleting sensitive memories | `TestMemorySettings*` |
| 6 | Refusals say what happened and what to run | `TestMemoryRefusals` |
| 7 | The built binary runs the commands against a stub of the routes with auth's mint | `cmd/latere.TestMemoryE2E` |

## Outcome

Built as designed, on sdk-go's main commit `e9cc3e9`, the pseudo-version
`v0.14.1-0.20261029202514-e9cc3e90c2cb`, until an sdk-go release carries
the package. Not run against the live API: memory's routes are reached
only by a throwaway account's run, which the design's acceptance names.
