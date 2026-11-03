---
title: "`latere storage`, `latere parsing` and hosted `latere agents`: the commands the CLI still lacks for three capabilities"
status: in-progress
depends_on:
  - specs/011-capability-commands.md
  - specs/016-devices-command.md
  - specs/024-oversight-and-hosted-sessions.md
  - "CROSS-REPO: sdk-go storage/ package, main after v0.30.0; copy, version restore, the owner's own shares, public links, the trash and starred files have no client method yet, though the Storage API serves all of them"
  - "CROSS-REPO: sdk-go has no parsing package; Lectio's API at api.latere.ai/v1/parsing (lectio specs/003-api.md) needs one before this spec's parsing commands can be built"
  - "CROSS-REPO: sdk-go agents/ package, main after v0.30.0: CreateSession, SendMessage, StreamEvents and EndSession, which this spec's hosted commands call"
affects:
  - internal/commands/storage.go, storage_test.go (new)
  - internal/commands/parsing.go, parsing_test.go (new)
  - internal/commands/agents_hosted.go, agents_hosted_test.go (new: start, send, follow, end)
  - internal/commands/agents.go, root.go (the new command groups and verbs registered)
  - internal/commands/apierror.go (the exit codes this spec's commands share with evals and oversight)
  - cmd/latere/storage_e2e_test.go, parsing_e2e_test.go, agents_hosted_e2e_test.go (new)
  - go.mod (sdk-go at whatever release adds the storage and parsing calls this spec still needs)
  - docs/storage.md (new), docs/parsing.md (new), docs/agents.md, README.md, CHANGELOG.md
created: 2026-11-03
author: changkun
---

# Storage, parsing and hosted agents: the commands the CLI still lacks

The platform serves three capabilities the CLI does not reach yet, or
reaches only in part. Storage (Arca) keeps a person's files at
`https://api.latere.ai/v1/storage`; nothing here lists, uploads, downloads,
moves, copies, deletes, shares or links them. Parsing (Lectio) turns a file
into a document at `https://api.latere.ai/v1/parsing`; nothing here submits
one. Agents (Topos) runs hosted sessions at `https://api.latere.ai/v1/agents`;
spec 024 added `latere agents sessions` and `latere agents session <id>`,
which read a person's hosted sessions and how each tool call was decided,
but nothing here starts one, sends it a message, follows it live, or ends
it. This spec is the command grammar for all three. Nothing in it is
built; it exists so the next spec that builds one of these groups starts
from a settled design rather than three open questions at once.

## Current state

| Fact | Where |
|---|---|
| `latere` has no `storage` or `parsing` command group | `internal/commands/root.go` |
| sdk-go's `storage` package writes, reads, checks and lists a file; moves a file or a folder; deletes one to the trash or for good; starts, puts, completes and aborts an upload in parts; `UploadFile` picks one request or parts by size on its own; and `ListSharedWithMe` reads what other people shared with you | sdk-go `storage/storage.go`, `manage.go`, `upload.go`, `shares.go` |
| The Storage API also serves a copy (`copy_to` on the same route a move's `move_to` is), a version restore (`restore_version`), the owner's own shares and public links (`POST`/`GET`/`DELETE /shares`, `/shares/links/*`), the trash (`GET`/`DELETE /trash`, `POST /trash/restore`) and starred files (`/stars`); sdk-go has no client method for any of them yet | sdk-go `openapi/storage.json`; `storage/*.go` has no `Copy*`, `Restore*`, `CreateShare`, `*Link*`, `*Trash*` or `*Star*` |
| sdk-go has no `parsing` package and no `openapi/parsing.json`; the API is Lectio's own, documented in Lectio's `specs/003-api.md` (submit, state and progress, a document as JSON, Markdown or text, chunks as newline-delimited JSON, and server-sent events) | sdk-go tree; `lectio` repository, `specs/003-api.md` |
| `latere agents sessions` and `latere agents session <id>` (spec 024) read a person's hosted sessions and how each tool call was decided, through sdk-go's `agents` package; they create nothing, send nothing and end nothing | `internal/commands/agents_sessions.go` |
| sdk-go's `agents` package already has `CreateSession`, `SendMessage`, `StreamEvents` and `EndSession`, which these hosted commands need and spec 024 did not call | sdk-go `agents/agents.go` |
| `latere agents run` is the agent on this machine, on the current directory's files, with a local model credential; spec 011 gave it no `--local` flag, since running locally is what the command is | spec 011; `internal/commands/topos_local.go` |
| `latere devices connect` names this computer as one of a person's environments, which a hosted session can run commands on through `Machine.Kind MachineDevice` | spec 016; sdk-go `agents.Machine` |

## Design

Every command in this spec takes `--json`, which prints the API's answer
as it came, the convention `latere memory` and `latere oversight` already
follow. Every group takes `--api-url` and reads an environment variable for
its address and a second for a bearer that is presented as given, following
`internal/commands/agents_sessions.go`'s pattern: `LATERE_STORAGE_URL` and
`LATERE_STORAGE_TOKEN` for storage, `LATERE_PARSING_URL` and
`LATERE_PARSING_TOKEN` for parsing. The hosted agents verbs reach the same
address as `agents sessions` and take the same `LATERE_AGENTS_URL` and
`LATERE_AGENTS_TOKEN`, since they are one more way of using the same API.
Each mints a token for the audience `api.latere.ai` from the saved login
when no token is given, as `latere repos` and `latere oversight` do.

### `latere storage`

A path always starts with `files/`, as the API requires, and `--owner`
names a space: `me`, the default, or an organization's id. `storage` picks
no default local path; a command that writes to disk takes `-o FILE`, with
`-` for standard output, as `latere environments export` does.

| Command | SDK call | Does |
|---|---|---|
| `storage ls [path] [--owner O] [--query Q] [--recent] [--limit N]` | `ListFilesPage` | lists the files and folders under path, `files` by default, in owner's space |
| `storage stat <path> [--owner O]` | `StatFile` | the file's size, content type, checksum and modified time, without its bytes |
| `storage download <path> [-o FILE] [--owner O] [--url] [--attachment]` | `ReadFile`, or `GetDownloadURL` with `--url` | streams the file to FILE or stdout; `--url` prints a link that serves it without a credential instead, `--attachment` making it save under its name rather than open |
| `storage upload <local-file> <path> [--owner O] [--content-type T] [--if-match H] [--if-none-match]` | `UploadFile` | writes a file of any size up to `MaxFileBytes`, in one request or in parts above `MaxWriteBytes` of its own accord, with a progress bar on stderr from `UploadOptions.Progress`; not a gap, since `UploadFile` already handles a large file |
| `storage mv <path> <to> [--owner O] [--recursive]` | `MoveFile`, or `MoveFolder` with `--recursive` | renames or moves a file, or with `--recursive` a folder and everything under it; copies no bytes |
| `storage cp <path> <to> [--owner O] [--recursive]` | none yet (CROSS-REPO: sdk-go `CopyFile`, `CopyFolder`) | copies a file, or with `--recursive` a folder, which the API's `copy_to` and `recursive` already do on the same route as a move |
| `storage rm <path> [--owner O] [--recursive] [--permanent] [--yes]` | `DeleteFile`, or `DeleteFolder` with `--recursive` | moves to the space's trash, or with `--permanent` removes it and its versions for good, after a yes unless `--yes` |
| `storage trash list [--owner O] [--limit N]` | none yet (CROSS-REPO: sdk-go trash listing, `GET /v1/storage/trash`) | lists what is in the space's trash |
| `storage trash restore <path> [--owner O]` | none yet (CROSS-REPO: sdk-go trash restore, `POST /v1/storage/trash/restore`) | brings a trashed file or folder back to where it was |
| `storage trash empty [--owner O] [--yes]` | none yet (CROSS-REPO: sdk-go trash empty, `DELETE /v1/storage/trash`) | deletes everything in the trash for good, after a yes unless `--yes` |
| `storage shares list [--owner O] [--limit N]` | none yet (CROSS-REPO: sdk-go `GET /v1/storage/shares`) | lists the shares this space's owner gave out |
| `storage shares create <path> <grantee> [--permission read\|write\|manage] [--expires T]` | none yet (CROSS-REPO: sdk-go `POST /v1/storage/shares`) | shares a file or folder with a person, a service or an organization, at a rung of `read`, `write` or `manage` |
| `storage shares revoke <id> [--owner O]` | none yet (CROSS-REPO: sdk-go `DELETE /v1/storage/shares/{id}`) | takes a share back; the share is kept, marked revoked |
| `storage shares with-me [--limit N]` | `ListSharedWithMe` | lists what other people shared with you; the one call of this group sdk-go already has |
| `storage links create <path> [--permission read\|write] [--expires T]` | none yet (CROSS-REPO: sdk-go `POST /v1/storage/shares/links`) | makes a public link to a file or folder, open to anyone who holds it |
| `storage links list [--owner O] [--limit N]` | none yet (CROSS-REPO: sdk-go `GET /v1/storage/shares/links`) | lists the links this space's owner made |
| `storage links revoke <id> [--owner O]` | none yet (CROSS-REPO: sdk-go `DELETE /v1/storage/shares/links/{id}`) | takes a link back; a copy already read keeps working until it expires if the API caches it, which `docs/storage.md` says plainly once this is built |

`storage ls` prints a table of path, size, content type and modified time,
cut like `latere memory list`'s text column, with folders listed by their
prefix and no size. `storage stat`, `shares create`, `shares revoke`,
`links create` and `links revoke` print the one object or nothing but a
confirmation; every list command pages on its own and prints what it
read, not one API page at a time, as `latere oversight cases` already
does for Limen's pages.

### `latere parsing`

Lectio's envelope and routes are in `lectio` repository `specs/003-api.md`;
this spec scopes the CLI to what the task names: submitting a file,
watching the parse, and reading the result. It does not cover retries,
figure description, structured extraction or sets, which stay for a later
spec once sdk-go's `parsing` package exists to build any of this on.

| Command | SDK call | Does |
|---|---|---|
| `parsing submit <local-file> [--reader R] [--pages SEL] [--class interactive\|batch] [--priority N] [--deadline D] [--label K=V ...] [--wait SECONDS]` | none yet (CROSS-REPO: sdk-go parsing package, `POST /files` then `POST /parses`) | uploads the file and submits a parse of it; `--wait` holds the answer until the parse ends or the wait runs out, as `Prefer: wait=` does, so a short document is one call |
| `parsing show <id>` | none yet (CROSS-REPO: sdk-go `GET /parses/{parse}`) | the parse's state, stage, pages done and failed, reused pages and usage |
| `parsing watch <id>` | none yet (CROSS-REPO: sdk-go `GET /parses/{parse}/events`, server-sent events) | follows the parse live: a line per page as it settles, and the state when it changes, until the parse ends |
| `parsing list [--state S] [--file F] [--label K=V] [--limit N]` | none yet (CROSS-REPO: sdk-go `GET /parses`) | lists parses newest first, filtered as the API allows |
| `parsing document <id> [--format json\|markdown\|text] [--pages SEL] [--tables auto\|markdown\|html] [--repeated once\|keep\|drop] [--page-breaks] [-o FILE]` | none yet (CROSS-REPO: sdk-go `GET /parses/{parse}/document`) | the document's index as JSON, or its content as Markdown or text, to FILE or stdout |
| `parsing chunks <id> [--by section\|page] [--max-chars N] [-o FILE]` | none yet (CROSS-REPO: sdk-go `GET /parses/{parse}/chunks`) | the document cut into chunks, one JSON object per line, to FILE or stdout |
| `parsing cancel <id>` | none yet (CROSS-REPO: sdk-go `POST /parses/{parse}/cancel`) | stops the work; pages already read are kept |
| `parsing rm <id> [--yes]` | none yet (CROSS-REPO: sdk-go `DELETE /parses/{parse}`) | deletes the parse and everything it wrote, after a yes unless `--yes`; refused while the parse has not ended |

`--json` on `show`, `list` and `watch` prints the raw object or objects;
`document --format json` already answers JSON, so `--json` on it changes
nothing. `chunks` is newline-delimited JSON either way, matching the API's
own `application/x-ndjson`, so it takes no `--json`. `watch` without
`--json` prints one line per page settled and one line per state change,
in the API's own order, and exits when the parse reaches a terminal
state; with `--json` it prints the server-sent event's data as one JSON
object per line, so a script can read it the same way `latere agents
follow --json` reads a hosted session's events, below.

### Hosted `latere agents`

Four verbs reach the platform's hosted plane, beside `agents sessions` and
`agents session <id>`, which spec 024 built and this spec does not change:

| Command | SDK call | Does |
|---|---|---|
| `agents start <agent> [-m MESSAGE] [--title T] [--attended] [--device D] [--workdir P] [--label K=V ...]` | `CreateSession` | starts a hosted session of the named agent, with an optional first message; `--device` and `--workdir` set `Machine` to run the session's commands on a connected computer (spec 016), left to the agent when neither is given |
| `agents send <session-id> <message> [--send-now]` | `SendMessage` | sends a message to a running or idle session; `--send-now` asks a running turn to read it before anything else, as `latere agents run`'s own Ctrl-J does locally (spec 023) |
| `agents follow <session-id> [--from-seq N] [--deltas]` | `StreamEvents` | follows the session's events live, from the start or from `--from-seq`, reconnecting where it left off; `--deltas` also yields the model's answer as it is written |
| `agents end <session-id> [--reason completed\|canceled]` | `EndSession` | ends a session so it takes no more messages; `--reason` defaults to `completed` |

`start` and `send` print the session or the event they created; with
`--json`, the raw object. `follow` prints a line per event: its sequence,
type and, for a message, its text; a tool call's event names the tool,
and `latere oversight explain` and `latere agents session <id>` are named
in the same line for how it was decided, as `agents session` already
does. With `--json` it prints one JSON object per event, so a script can
pipe it through `jq` the way `latere agents session --json` is read today.

### Local and hosted, with no overlap

`agents run` stays exactly what spec 011 made it: the Latere agent on
this machine, on the current directory's real files, with a local model
credential, and no `--local` flag, since running locally is what the
command is. None of `start`, `send`, `follow` or `end` takes `--dir` or
`--model`, which are `run`'s own flags for a local working directory and a
local model provider; a hosted session's working directory is the
`Machine` it runs on, named once at `start`, and its model is the agent's
own configuration on the platform, not a flag of this CLI. A session
`start` creates cannot be resumed by `agents run`, which has no session of
its own to resume; it is inspected with `agents session <id>` and spoken
to with `send` and `follow`. `agents review` is unaffected: it is a local
adversarial review of a Claude Code session, not a hosted one.

### Errors

Every command in this spec exits 0 done, 2 refused, 3 no answer: the
exit codes `apierror.go` already gives `latere evals` and `latere
oversight`, shared rather than reinvented. A refusal's `message` is the
API's own fixed sentence; a developer detail, when the API gives one,
goes in a separate field of `--json`'s output, never folded into the
printed sentence, following the registers rule in AGENTS.md. A 401 names
`latere login`. `storage rm --permanent`, `storage trash empty`,
`parsing rm` and `agents end` ask `[y/N]` on standard error before they
act, as `latere memory rm` does, unless `--yes` is given or there is no
terminal to ask on, in which case they refuse with the `--yes` that
would have skipped the question.

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| Copy, trash, shares, links and parsing wait on sdk-go | the command grammar is written now; the SDK method is a CROSS-REPO dependency named per command | writing the HTTP calls straight into the CLI: `pkg stays lean` and the family's own rule keep server and client logic out of a command's own file; the SDK is where every other capability's calls live |
| Hosted verbs are `start`, `send`, `follow`, `end`, not `sessions <verb>` | `sessions` and `session <id>` stay the oversight read views spec 024 gave them; the new verbs sit beside them under `agents` | nesting under `sessions`: that command's job is listing and inspecting, and a write verb under a read noun reads backward, as `latere apps` keeps `create` separate from `list` |
| `storage upload` needs no new design for a large file | `UploadFile` already picks parts above `MaxWriteBytes` and reports progress | a CLI-side chunker: the SDK already does this; repeating it would be the thin-wrapper problem in reverse, logic duplicated instead of called |
| `parsing watch` is one command, not `show --follow` | a verb of its own, since it follows a live stream rather than printing one answer | a `--follow` flag on `show`: `show` is one answer, `watch` is a stream; `latere environments logs -f` already shows the CLI making that a separate behavior, not a flag that changes a command's shape |

## Not in this spec

- Starred files (`/stars`), the activity log (`/storage/events`), file
  materialization and the storage admin routes.
- Lectio's figure description, structured extraction, sets, stored
  readers and the routing policy.
- A hosted session's fork, archive, resume, redact, search or summary,
  which spec 024 did not add either and which wait for their own design.
- Changing what `agents sessions` or `agents session <id>` show; this
  spec only adds verbs beside them.
- `agents run`'s local review and provider commands, unaffected.

## Acceptance criteria

| # | Criterion | Test | Status |
|---|---|---|---|
| 1 | `storage ls`, `stat` and `download` (bytes and `--url`) call the SDK methods named above and print a table, an object and a stream or a link respectively | `TestStorageLs`, `TestStorageStat`, `TestStorageDownload` | not built |
| 2 | `storage upload` writes a small file in one request and a large one in parts, with a progress bar on stderr, and fails the write it cannot complete | `TestStorageUploadSmall`, `TestStorageUploadLarge` | not built |
| 3 | `storage mv` and `storage cp` move or copy a file and, with `--recursive`, a folder; `cp` is refused with a named CROSS-REPO reason until sdk-go has `CopyFile`/`CopyFolder` | `TestStorageMove`, `TestStorageCopyNotBuilt` | not built |
| 4 | `storage rm` moves to trash by default, asks before a permanent delete, and skips the question with `--yes` | `TestStorageRemoveAsksFirst`, `TestStorageRemoveYes` | not built |
| 5 | `storage trash list`, `restore` and `empty` are refused with a named CROSS-REPO reason until sdk-go has the trash calls | `TestStorageTrashNotBuilt` | not built |
| 6 | `storage shares list`, `create`, `revoke` and `links list`, `create`, `revoke` are refused the same way; `storage shares with-me` calls `ListSharedWithMe` and lists its pages | `TestStorageSharesNotBuilt`, `TestStorageSharesWithMe` | not built |
| 7 | Every `parsing` command is refused with a named CROSS-REPO reason until sdk-go has a `parsing` package, and the refusal names the missing package rather than failing some other way | `TestParsingNotBuilt` | not built |
| 8 | `agents start` creates a session, with `--device` and `--workdir` setting `Machine`, and prints it; `agents send` sends a message and prints the event | `TestAgentsStart`, `TestAgentsStartOnADevice`, `TestAgentsSend` | not built |
| 9 | `agents follow` streams events from the start and from `--from-seq`, reconnecting where it left off, text by default and one JSON object per line with `--json` | `TestAgentsFollow`, `TestAgentsFollowResumes`, `TestAgentsFollowJSON` | not built |
| 10 | `agents end` ends a session with the reason given, defaulting to `completed`, and refuses a reason that is neither `completed` nor `canceled` before any call | `TestAgentsEnd`, `TestAgentsEndRefusesABadReason` | not built |
| 11 | No command here changes what `agents run`, `agents sessions` or `agents session <id>` do; a command-tree test holds the old verbs' flags and help text to what spec 011 and 024 gave them | `TestAgentsRunUnaffectedByThisSpec` | not built |
| 12 | Every command exits 0, 2 or 3 as `apierror.go` defines them, and a destructive command without a terminal refuses rather than deletes | `TestStorageParsingAgentsExitCodes` | not built |
| 13 | The built binary runs the built commands (`storage ls/stat/download/upload/mv/rm/shares with-me`, `agents start/send/follow/end`) against a stub of their APIs with auth's mint | `cmd/latere.TestStorageE2E`, `cmd/latere.TestAgentsHostedE2E` | not built |

## Outcome

Not built. This spec records the design; a later spec, or an update to
this one, reports what shipped once sdk-go's storage additions, its
parsing package and the hosted agents verbs are built.
