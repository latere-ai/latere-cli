# Topos

[Topos](https://topos.latere.ai) is the Latere agent platform. With `latere topos`
you run coding-assistant sessions whose work happens on the Topos control plane,
not your laptop — so you can start a session, close your terminal, and pick it
back up later, on the same machine or another one, right where it left off.

Run `latere login` first (see the [main README](../README.md#sign-in)). For
local development against a Topos server started with `TOPOS_DEV_AUTH=true` and
`TOPOS_DEV_TOKEN=<secret>`, set `TOPOS_API_URL` and `TOPOS_TOKEN` to talk to it
directly:

```sh
export TOPOS_API_URL=http://localhost:8080
export TOPOS_TOKEN=<secret>
```

## An interactive session

Start a session on an agent and you drop into a terminal UI — type a message,
watch the agent think and run tools, and steer it as it goes:

```sh
latere topos session start agent_01hxy
```

Inside the session:

- **Type and press Enter** to send a message. The reply streams in token by token.
- **Esc** interrupts the current turn (the agent stops what it is doing; nothing
  you have said is lost).
- When the agent wants to run a tool that policy flags for review, you get an
  inline **`approve tool …? [y/n]`** prompt — press `y` to allow it or `n` to deny.
- **Ctrl+C** detaches. The session keeps running on the server; the screen shows
  you the command to reattach.

## Detach and reattach

Detaching never stops the work. Reattach any time — from anywhere — and you get
the full history followed by whatever has happened since:

```sh
latere topos session attach sess_01hxy
```

List the sessions you can attach to, with their current state:

```sh
latere topos session ls
# sess_01hxy  awaiting_input    agent_01hxy
# sess_02abc  running           agent_07def
```

Attach as a read-only viewer (watch without being able to type):

```sh
latere topos session attach sess_01hxy --readonly
```

If your connection drops, the client reconnects on its own and resumes from where
you were — you should not have to do anything.

## Print mode (scripts and pipelines)

For automation, run one prompt non-interactively with `--print`/`-p`. The agent's
answer streams to stdout (tool activity goes to stderr), and the command exits
when the turn finishes — so it composes in shells and CI, like `claude -p`:

```sh
latere topos session start agent_01hxy -p "summarise README.md" > summary.md
```

Send a follow-up turn to an existing session the same way:

```sh
latere topos session attach sess_01hxy -p "now write the tests"
```

Print mode waits through session replay and reports the response to your new prompt.

`--readonly` cannot be combined with `--print`/`-p`, which sends a new prompt.

Print mode exits non-zero if the agent reports an error, the server rejects the
request, the connection ends before completion is confirmed, or streamed output
cannot be written. Any text already streamed stays on stdout; check the exit
status before using it in later CI steps.

## Autonomous runs

If you just want to fire one self-contained run and read the result (no back and
forth), use `session create`:

```sh
latere topos session create agent_01hxy --prompt "List the repo files."
```

## Managing agents

```sh
latere topos agents list
latere topos agents get agent_01hxy
latere topos agents create --name "Build Bot" --kind worker \
  --instructions "You triage CI failures."
```

## Where things run

Your laptop is just the screen and keyboard. The agent's reasoning, its tools,
and its workspace all live on the control plane, which is why a session survives a
disconnect and why a teammate can watch the same session you are driving. The
base URL defaults to `https://topos.latere.ai`; override it with `TOPOS_API_URL`
or `--api-url` on any command.
