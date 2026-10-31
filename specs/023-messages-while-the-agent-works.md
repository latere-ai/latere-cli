---
title: "`latere agents run`: a message typed while the agent works waits for its next step, shows where it was read, can be sent now or taken back"
status: in-progress
depends_on:
  - specs/011-capability-commands.md
  - "CROSS-REPO: topos specs/098-messages-sent-while-a-turn-runs.md, shipped in topos v0.62.0: messages.read, user.withdraw, send_now, the send_now stop"
affects:
  - go.mod (topos v0.62.0, sdk-go v0.19.0)
  - internal/commands/topos_local.go (send, withdraw, drive, record, the send_now stop)
  - internal/commands/topos_local_tui.go (Enter while it works, the waiting area, Up, Ctrl+J)
  - internal/commands/topos_local_test.go, internal/commands/topos_local_tui_test.go
  - docs/agents.md, CHANGELOG.md
created: 2026-10-31
updated: 2026-10-31
author: changkun
---

# Messages typed while the local agent works

`latere agents run` runs the Topos runner and harness in process on an
in-memory log. Before this spec the TUI ignored Enter while a turn ran, and
`turn` reported every stop but `end_turn` and `completed` as an error. Topos
v0.62.0 reads a person's message sent while a turn runs at the turn's next
step (spec 098), so the local agent can take one, as Claude Code does.

## Current state

| Fact | Where |
|---|---|
| Before each request of the session's own thread, the harness appends `messages.read` naming the unread waiting messages of a person, then folds the log that append returned | topos `harness/waiting.go`, `markReads`; `harness/harness.go`, `stepOnce` |
| A turn that ends with such a message unread reports `Outcome.Pending`, and `runner.Drive` runs the next turn without returning | topos `harness/harness.go`, `pending`; `runner/runner.go`, `drive` |
| The runner's watch of the store sends a person's `user.message` with `send_now` to the harness when the config leaves `SendNow` nil, which the local config does | topos `runner/runner.go`, `interrupts.watch` |
| A send_now during a request cancels it; during calls it moves running `bash` commands to the background (the host machine moves them in process), waits `harness.SendNowGrace`, then cuts the rest; a cut ends the turn `interrupted` with detail `send_now` | topos `harness/harness.go`, `interruptible` |
| The server checks a `user.withdraw` against the log it is appended after and retries on a sequence conflict; that check is in `internal/server`, which a client cannot import | topos `internal/server/events.go`, `withdrawable`; `sessions.go`, `appendChecked` |
| A message's state, waiting, read or withdrawn, and whether a send_now cut its turn, is read from the log alone | topos `session/waiting.go`, `WaitingMessages` |

## Design

The agent:

| Call | Behavior |
|---|---|
| `send(text, now)` | appends a person's `user.message` after the log's last event, again on a sequence conflict, with `send_now` when `now` is set and a drive runs; says whether the caller has to start a drive |
| `drive()` | runs `runner.Drive` and, once it returns, runs it again while `session.HasPendingInput` holds, so a message appended as a turn ends starts the next; `send` and the last check share a lock, so no message is left with nobody to read it |
| `withdraw(id)` | appends `user.withdraw` when `session.WaitingMessages` says the message is waiting and its sender is the person, as the server's check does, on the log it is appended after; a read message is `errAlreadyRead` |
| `turn(text)` | `send` then `drive`, the REPL's and `-p`'s path |

A stop `interrupted` with detail `send_now` is the person's choice, not an
error. `record` tells the terminal a message that starts a turn, and each
message a `messages.read` names, with whether a send_now cut its turn.

The TUI, after Claude Code:

| Key | While it works | Otherwise |
|---|---|---|
| Enter | sends the message, which shows below the conversation and above the prompt, dim, with "reads this after its current step" | starts a turn, as before |
| Ctrl+J, Alt+Enter | sends the message to be read now | as Enter |
| Up, prompt empty, messages waiting | takes the waiting messages back into the prompt; one already read stays, with "had already read this, so it stays" | scrolls, as before |
| Esc, Ctrl+C, Ctrl+D | unchanged | unchanged |

A waiting message moves into the transcript when a `messages.read` names
it, at that place; one a send_now cut the turn for carries "stopped what it
was doing to read this".

## Decisions

| Decision | Chosen | Alternatives and why not |
|---|---|---|
| The send-now key | Ctrl+J, with Alt+Enter beside it | Shift+Enter or Ctrl+Enter: most terminals send the same carriage return for both as for Enter. Ctrl+J is a line feed, which every terminal sends apart from Enter's carriage return in raw mode. Alt+Enter needs the terminal to send Option or Alt as Esc |
| A message's state in the TUI | read from the log's events | kept by the TUI from what it sent: a message that lands after a turn's last status starts the next turn and is never named by a `messages.read`, which only the log tells |
| The withdraw check | the server's rule, run on the in-memory log | none: a withdrawal and the harness's `messages.read` of the same message could both stand |
| Messages taken back with Up | joined with a space in the one-line prompt | one at a time: Claude Code takes them all back |

## Not in this spec

A sessions command that follows a hosted session's log: the CLI has none.
The REPL and `-p` read input only between turns, so they send nothing while
a turn runs.

## Acceptance criteria

| # | Criterion | Test |
|---|---|---|
| 1 | A message sent while a command runs is read at the next step of the same turn, led by the while-working line, and shown where it was read | `TestLocalMessageWhileACommandRunsIsReadAtTheNextStep` |
| 2 | Send now moves a running command to the background and the same turn reads the message | `TestLocalSendNowMovesACommand` |
| 3 | Send now cuts a call that cannot move after the grace; `turn` returns no error and follows the next turn, which reads the message led by the sent-now line | `TestLocalSendNowCutIsNotAnError` |
| 4 | A stop by send_now is no error; any other stop but `end_turn` and `completed` is | `TestStopErrorOfASendNowStop` |
| 5 | A message appended after a turn's last status is driven by the drive that saw it | `TestLocalDriveReadsAMessageAsATurnEnds` |
| 6 | A waiting message is withdrawn and never read; a read one, one that started its turn and one already withdrawn are refused | `TestLocalWithdraw` |
| 7 | The TUI sends while it works, shows a waiting message above the prompt and moves it into the transcript when read, sends now on Ctrl+J and Alt+Enter, takes waiting messages back on Up, and keeps a read one with its note | `TestLocalTUIMessagesWhileItWorks`, `TestLocalTUIUpTakesBackWaitingMessages`, `TestLocalTUISendNowKeys` |
| 8 | End to end in the TUI over the scripted model: a message typed while a command runs waits, then shows where it was read | `TestLocalTUIMessageWhileACommandRunsEndToEnd` |
