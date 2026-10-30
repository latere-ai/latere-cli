---
title: "`latere memory tidies`: see what Tidy memory changed each night, undo it, apply or decline what waits, and choose its review mode and Search past chats"
status: in-progress
depends_on:
  - specs/020-memory-command.md
  - "CROSS-REPO: latere-ai/specs infrastructure/memory.md, section 15: dreaming, the CLI's commands in its surfaces table, and its Outcome as Tidy memory"
  - "CROSS-REPO: platform specs/235: the runs routes under /v1/account/runs, their refusals, and the review mode in the memory settings"
  - "CROSS-REPO: sdk-go v0.16.0 memory/: ListRuns, GetRun, ApplyRun, DeclineRun, UndoRun, Settings.Review and Settings.Chats"
affects:
  - internal/commands/memory_tidies.go (new: the group, its commands, the readable file and the diff)
  - internal/commands/memory_tidies_test.go (new)
  - internal/commands/memory.go (the group registered, the help, a tidy as a memory's source, the run codes in refusals)
  - internal/commands/memory_settings.go (chats labeled, review read and set)
  - internal/commands/memory_settings_test.go
  - cmd/latere/memory_e2e_test.go
  - docs/memory.md, README.md, CHANGELOG.md
  - go.mod (sdk-go v0.16.0)
created: 2026-10-30
updated: 2026-10-30
author: changkun
---

# Tidy memory from the CLI

Tidy memory is an Always on item: each night it goes through one scope
of a person's memory, their own or one project's, combines memories that
say the same thing, settles ones that contradict each other, and forgets
ones that only recorded a finished task. Each night's work on a scope is
a run with what it changed, which the person can undo; a change to a
memory the person wrote waits for their OK, and the review mode `ask`
holds every change. This records the CLI's commands for it, which 020
left for when the runs existed.

## Current state

| Fact | Where |
|---|---|
| `GET /v1/account/runs` lists the runs, newest first, at most 50, each with its state, review mode and counts, without its changes | sdk-go `memory.ListRuns` |
| `GET /v1/account/runs/{id}` answers one run with each change: the memory's id, kind, state, why it is held, and the memory's file before and after | `memory.GetRun` |
| apply and decline decide waiting changes and are sent once; undo is retried, since a repeat undoes nothing more | `memory.ApplyRun`, `DeclineRun`, `UndoRun` |
| A run's id is the id of the session it ran in, `ses_…` | `memory.Run.ID` |
| What a run wrote carries the source `dream:<run id>` | platform `internal/memory/runs.go`, `SourceRunPrefix` |
| The settings answer `chats`, a switch, and `review`, `apply` or `ask` | `memory.Settings` |
| The console's words for runs, changes and their states | platform `frontend/src/console/account/memory/words.ts` |

## Design

### Commands

| Command | SDK call | Does |
|---|---|---|
| `memory tidies [--project P]`, or `tidies list` | `ListRuns`, `ListMemories` for project names | every tidy, newest first: its id, whose memory, when, its state and what it changed |
| `memory tidies show <tidy>` | `GetRun`, `ListMemories` for a project's name | the tidy, then each change: what it did, where it stands, why it waits, and the memory before and after as a line diff |
| `memory tidies apply <tidy> [memory ...]` | `GetRun`, `ApplyRun` | applies what waits, the memories named or every one |
| `memory tidies decline <tidy> [memory ...]` | `GetRun`, `DeclineRun` | declines what waits |
| `memory tidies undo <tidy> [memory ...] [--yes]` | `GetRun`, `UndoRun` | takes back what was applied; more than one change asks first |
| `memory settings chats=on\|off`, `review=apply\|ask` | `UpdateSettings` | Search past chats and the review mode, beside the switches 020 shows |

Each takes `--json`, which prints the platform's answer as it came.

### Names

The product calls the item Tidy memory and one night's work on a scope a
tidy, as the console does ("There is no tidy here that you can open"), so
the group is `tidies`, a plural noun with verbs under it, as
`latere evals experiments` and `latere environments secrets` are. The
family design wrote `latere memory runs` and `latere memory undo <run>`;
"run" is the mechanism's word, and a bare `memory undo` beside `rm` and
`restore` would read as undoing an edit. Bare `tidies` lists, as
`restore` without an id does, and `tidies list` is the same command for
a person who types it from `memory list`.

A change is named by its memory's id, as the run names it, so deciding
one reads `latere memory tidies undo <tidy> mia`.

### What a decision prints

Each decision reads the tidy first, so the command says what the
decision did: how many changes it applied, declined or undid, and which
were left as they were because the person changed the memory since
(`changed_since` on an apply, `Undone.MovedOn` on an undo). It then
prints each change with where it stands now. Reading first also gives
undo its question: undoing more than one change asks `[y/N]` unless
`--yes`, as the console asks before Undo all.

apply and decline are sent once, and their answer can fail after the
decision is saved, so a failure that is not a refusal says the decision
may have been saved and names `show`, rather than inviting a repeat. A
`run_not_waiting` says nothing named waits any more, which is also what
a repeat of a decision that landed meets.

### The memory before and after

A change carries the memory's whole file. The CLI reads it as the
console does: the header's members as labeled lines, Name, Memory,
Group, From, About a sensitive topic, then any member it has no word for
by its key, then the detail after a blank line. A file without a header
is shown as it is. The two readings are compared line by line, with `-`
for a line only before and `+` for one only after; an added memory is
all `+`, a removed one all `-`. Control characters are dropped, as
everywhere memory text is printed.

### Where a memory came from

A memory a tidy wrote has the source `dream:<tidy>`. `memory list`
shows it as `tidy <id>` and `memory show` as Tidy memory with the
tidy's id, where they showed it as a conversation's id before.

### When

A tidy runs at night in the person's time zone, so the time it ran is
shown in the computer's local time, as `latere devices` shows its dates,
where `memory show` prints UTC.

### Settings

`chats` is a `*bool` of `UpdateSettingsParams`, so 020's switches take
it with no change but its label. `review` is a word, not a switch: the
settings read a string member as a setting whose state is its word, and
set a string member of the params the same way, refusing a word the CLI
knows is not one of the mode's (`apply`, `ask`) before anything is sent.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| The group's name | `tidies`, the product's word | `runs`, the family design's: a mechanism word; `tidy`: reads as a verb that tidies now |
| Deciding | `apply`, `decline`, `undo` under `tidies` | top-level `memory undo <run>`: ambiguous beside `rm` and `restore` |
| The output of a decision | what it did, counted from the tidy before and after, then each change's state | the whole `show`: the diffs again after every decision |
| Undo of more than one change | asks, `--yes` skips | never asks: the console confirms Undo all; always asks: one change is the console's single Undo |
| Decline | never asks | the console's Decline all does not ask either |
| When a tidy ran | local time | UTC, as `memory show`: a tidy at 03:00 in Asia reads as the evening before |

## Acceptance criteria

| # | Criterion | Test |
|---|---|---|
| 1 | `tidies` lists every tidy with whose memory, its state and the console's summary; `--project` keeps one project's; `--json` keeps the answer's shape | `TestMemoryTidiesList*` |
| 2 | `show` prints each change with its state, why it waits and the readable diff; a running tidy and one that changed nothing say so | `TestMemoryTidiesShow*`, `TestReadableMemory`, `TestLineDiff` |
| 3 | `apply`, `decline` and `undo` send the memories named, or none for every one, and say what changed and what was left | `TestMemoryTidiesDecide*` |
| 4 | Undo of more than one change sends nothing without a yes | `TestMemoryTidiesUndoAsksFirst` |
| 5 | The run codes are refused with what to run next; a decision whose answer failed says it may have been saved | `TestMemoryTidiesRefusals` |
| 6 | `settings` shows and sets `chats` and `review`, and refuses a review word that is neither | `TestMemorySettings*` |
| 7 | A memory a tidy wrote names the tidy as its source | `TestMemorySourceOfATidy` |
| 8 | The built binary lists, shows and decides a tidy against a stub of the routes with auth's mint | `cmd/latere.TestMemoryTidiesE2E` |

