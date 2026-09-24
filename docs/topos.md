# Topos

[Topos](https://topos.latere.ai) is the Latere agent platform. `latere topos`
runs a coding agent in one of two places:

- **On this machine**, with `latere topos --local`. The agent works in your
  directory on your real files, like Claude Code. No control plane and no
  Latere sign-in are required.
- **On the hosted platform**, with `latere topos`. The agent's reasoning,
  tools, and workspace live on the control plane, so you can start a
  session, close your terminal, and pick it up later, on this machine or
  another one, where it left off.

## On this machine

```sh
latere topos --local                               # an interactive session in the current directory
latere topos --local --dir ~/code/project          # in another directory
latere topos --local -p "add a test for foo()"     # run one prompt, stream the result, and exit
latere topos --local --model claude-sonnet-4-6     # pick the model
```

The agent reads, edits, and runs commands directly in the working
directory, as you, with no isolation and no approval prompt. Run it where
you would run a coding assistant of your own, not in a directory you do
not want changed.

Inside the interactive session, `/model <name>` switches the model,
`/model` alone lists the Anthropic models Lux offers you, `/help` lists the
commands, and `/quit` leaves.

### Choosing the model

`--local` uses the first of these that is available:

1. `ANTHROPIC_API_KEY`, calling Anthropic directly.
2. The provider you chose with `latere topos login`.
3. Lux, when you are signed in with `latere login`: the call goes through
   the gateway on your identity and is billed to it, with no provider key
   on your machine. This is the default once you are signed in.
4. `CLAUDE_CODE_OAUTH_TOKEN`, the token Claude Code uses, which shares
   Claude Code's rate limit.

With none of them, an interactive session opens the provider picker, and
`-p` or a run without a terminal stops with an error that names what to
set.

```sh
latere topos login
```

opens the same picker: sign in with Claude in your browser, paste an
Anthropic API key, or use Ollama for models running on this machine. The
choice is saved in your user configuration directory and wins over
`CLAUDE_CODE_OAUTH_TOKEN`, so an API key or Ollama keeps the local agent
off Claude Code's shared rate limit.

## On the hosted platform

```sh
latere topos
```

opens the home screen: start a new session, or resume one that is running.
It signs you in on first use. A session started here belongs to you and
needs no agent set up first. Without a terminal, the command prints your
sessions and exits.

### An interactive session

Sessions can also run a named agent:

```sh
latere topos session start agent_01hxy
latere topos session start agent_01hxy --from-repo https://code.latere.ai/<owner>/<repo>.git
```

`--from-repo` starts the session from a server-side clone of that
repository. Inside the session:

- **Type and press Enter** to send a message. The reply streams in as it
  is written.
- **Esc** interrupts the current turn. The agent stops what it is doing,
  and nothing you have said is lost.
- When the agent wants to run a tool that policy flags for review, you get
  an inline **`approve tool …? [y/n]`** prompt: press `y` to allow it or
  `n` to deny it. Your draft is kept while the prompt is visible; Enter and
  editing keys do nothing until you decide, and a paste that arrives then
  is ignored, so paste again afterwards.
- **Ctrl+C** detaches. The session keeps running on the server, and the
  screen shows the command to reattach.

If a turn fails or is interrupted while text is streaming, the text already
received stays on screen, marked `(incomplete)`, and the next response
starts separately.

### Detach and reattach

Detaching never stops the work. Reattach at any time, from anywhere, and
you get the full history followed by whatever has happened since:

```sh
latere topos session attach sess_01hxy
latere topos session attach sess_01hxy --readonly   # watch without typing
```

List the sessions you can attach to, with their state:

```sh
latere topos session ls
# sess_01hxy  awaiting_input    agent_01hxy
# sess_02abc  running           agent_07def
```

If your connection drops, the client waits one second, reconnects, and
resumes where you were. If sending a message, an approval, or an interrupt
fails, the interface shows the error and keeps your message or pending
approval; once reconnected, press Enter, `y`/`n`, or Esc again to retry.
When the retries run out, the command exits with the last connection
error.

After a server restart, a notice shows the saved turn and says that
interrupted work was rolled back, and any old approval prompt is cleared.

### Print mode, for scripts and pipelines

`--print`/`-p` runs one prompt without the interface. The answer streams to
stdout, tool activity goes to stderr, and the command exits when the turn
finishes, so it composes in shells and CI like `claude -p`:

```sh
latere topos session start agent_01hxy -p "summarize README.md" > summary.md
latere topos session attach sess_01hxy -p "now write the tests"
```

Print mode waits through the session's replay and reports the answer to
your new prompt. `--readonly` cannot be combined with `-p`, which sends a
prompt.

Print mode exits non-zero, with any text already streamed left on stdout,
when:

- a tool needs your approval: attach without `-p` to approve or deny it;
- the session reaches its spending limit: the client reports
  `budget limit reached` with the spend and the limit when it has them,
  after reading to the end of the turn to keep the final partial answer;
- the model's output reaches its token limit, or the run ends while still
  asking for tools, and the result may be incomplete;
- the agent reports an error, the server refuses the request, a stream
  frame cannot be decoded, the connection ends before the turn is
  confirmed complete, or the output cannot be written.

Check the exit status before a later CI step uses the output.

### Autonomous runs

To fire one self-contained run and read its result, with no back and
forth:

```sh
latere topos session create agent_01hxy --prompt "List the repo files."
```

### Agents

```sh
latere topos agents list
latere topos agents get agent_01hxy
latere topos agents create --name "Build Bot" --kind worker \
  --instructions "You triage CI failures."
```

An agent's owner and organization come from your token; the client never
sends them.

## Lend this machine to a hosted session

```sh
latere topos serve-sandbox
latere topos serve-sandbox --root ~/work/project
```

connects this machine to the control plane as a sandbox, so a hosted
session runs its tools here, on your files. The connection is outbound; no
port is opened. Every command a remote session wants to run is shown and
waits for your `y` before it runs, one at a time, unless you pass `--yes`.
File access is confined to the root: a relative symlink that stays inside
it is followed, and an absolute symlink or one that leads outside is
refused. A built-in deny list, including `.env`, `.ssh`, and `*.pem`, is
never served. A request that disconnects while it waits for your answer is
discarded before the next one is shown.

## Settings

| Setting | Purpose |
|---------|---------|
| `--api-url` / `TOPOS_API_URL` | The hosted platform, `https://topos.latere.ai` by default. `serve-sandbox` takes `--topos-url`. |
| `TOPOS_TOKEN` | Present this bearer to Topos instead of minting one from your login. |
| `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` | Model credentials for `--local`, in the order above. |

[configuration.md](configuration.md) lists the files `latere topos login`
keeps.

### A development server

To work against a Topos server you run yourself with development
authentication (`TOPOS_DEV_AUTH=true` and `TOPOS_DEV_TOKEN=<secret>`),
point the CLI at it and present that secret:

```sh
export TOPOS_API_URL=http://localhost:8080
export TOPOS_TOKEN=<secret>
latere topos session ls
TOPOS_TOKEN=<secret> latere topos serve-sandbox --topos-url http://localhost:8080
```
