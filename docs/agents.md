# Agents

`latere agents` holds the commands of Latere Agents that run on this
machine: `latere agents run` runs the Latere agent in your directory on your
real files, `latere agents provider` chooses the model it calls, and
`latere agents review` runs an adversarial review of your latest Claude Code
session. Hosted agents, which run in their own workload with a session you
can follow, are created and run from the console and its API.

Until v0.16 these were `topos --local`, `topos login` and the top-level
`review`. For one release those words exit 1 and name the command that
replaced them; then they are removed.

## Running the agent

```sh
latere agents run                                   # an interactive session in the current directory
latere agents run --dir ~/code/project              # in another directory
latere agents run -p "add a test for foo()"         # run one prompt, stream the result, and exit
latere agents run --model anthropic/claude-sonnet-4.6   # pick the model
```

The agent reads, edits, and runs commands directly in the working
directory, as you, with no isolation and no approval prompt. Run it where
you would run a coding assistant of your own, not in a directory you do
not want changed. It needs no workload and no hosted session.

Inside the interactive session, `/model <name>` switches the model,
`/model` alone lists the models your model key reaches, `/help` lists the
commands, and `/quit` leaves.

### Choosing the model

`latere agents run` uses the first of these that is available:

1. `ANTHROPIC_API_KEY`, calling Anthropic directly.
2. The provider you chose with `latere agents provider`.
3. The Latere API, when you are signed in with `latere login` or hand the
   CLI a key with `LATERE_MODEL_KEY`: the call presents your
   [model key](models.md#your-model-key) and is billed to its context,
   with no provider key on your machine. This is the default once you are
   signed in. Name a model as `latere models` lists it; the default is
   `anthropic/claude-sonnet-4.6`.
4. `CLAUDE_CODE_OAUTH_TOKEN`, the token Claude Code uses, which shares
   Claude Code's rate limit.

With none of them, an interactive session opens the provider picker, and
`-p` or a run without a terminal stops with an error that names what to
set.

```sh
latere agents provider
```

opens the same picker: sign in with Claude in your browser, paste an
Anthropic API key, or use Ollama for models running on this machine. The
choice is saved in your user configuration directory and wins over
`CLAUDE_CODE_OAUTH_TOKEN`, so an API key or Ollama keeps the local agent
off Claude Code's shared rate limit. A choice saved by an earlier version
is moved to the current file the first time it is read.

## Reviewing a Claude Code session

`latere agents review` runs an adversarial review of your most recent Claude Code session: it forks the session as a *proposer* that defends the change, runs *critics* against the working-tree diff, and surfaces the attacks that survive. Run `latere login` first (see the [main README](../README.md#sign-in)).

The proposer runs locally through your own `claude` CLI for full fidelity (it forks your real session with `claude --resume <id> --fork-session`), while the critics call their model through the Latere API with your [model key](models.md#your-model-key), so critic model cost is drawn from your current context and no provider key is needed locally.

### Prerequisites

- `latere login` (the critics call models with your model key, which the CLI creates on first use).
- The [`claude`](https://code.claude.com/docs) CLI installed and authenticated (the proposer forks your real Claude Code session).
- A git repository with a recent Claude Code session in it.

### Quickstart

From inside a repo where you just finished a Claude Code session:

```sh
latere agents review
```

That reviews the most recent session under the working directory. To pick a specific session or run a deeper debate:

```sh
latere agents review --session 4f3c2b1a --forks 3 --max-rounds 6
```

The review looks at the working-tree diff (`HEAD` vs the tree); when the tree is clean (Claude already committed) it falls back to `HEAD~1..HEAD`. A trivial diff is skipped.

### Exit codes

`latere agents review` carries the review verdict in its exit code, so it can gate a script or hook:

```sh
latere agents review && git push     # push only if the review is clean
```

| Code | Meaning |
|------|---------|
| `0` | Debate completed with no unresolved attacks |
| `2` | Debate completed but left unresolved attacks |
| `1` | Command error (not signed in, no session, the Latere API unreachable or refusing the key) |

### Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `--session` | most recent under `--dir` | Claude Code session ID to review |
| `--dir` | `.` | Working directory (git repo root and Claude session home) |
| `--state-dir` | `$XDG_STATE_HOME/latere/reviews/<repo-key>/` | Where to write review logs |
| `--forks` | `1` | Number of independent critic forks |
| `--max-rounds` | `4` | Per-fork debate-round cap |
| `--cost-cap` | `50000` | Soft token budget (proposer tokens; the critics report no usage yet) |
| `--model` | `anthropic/claude-sonnet-4.6` | Critic model, named as `latere models` lists it |
| `--proposer-timeout` | `5m` | Per-round deadline for the proposer's claude call (large sessions may need more) |
| `--models-url` | `LATERE_MODELS_URL` or `https://api.latere.ai/v1/models` | Override the models base URL |
| `--auth-url` | derived from the models URL | Override the auth base URL |

### Review-log location

Review logs are transient state, so they are written to a user-global XDG state directory rather than into the repo you are reviewing:

```
$XDG_STATE_HOME/latere/reviews/<repo-key>/sessions/<session-id>/
  fallback: ~/.local/state/latere/reviews/<repo-key>/sessions/<session-id>/
```

`<repo-key>` is a stable, filesystem-safe key derived from the repository's git toplevel path (a readable slug plus a short hash), so each project's reviews accumulate under one findable folder and two repos with the same name never collide. Pass `--state-dir <path>` to write somewhere else for a one-off run; a user-chosen `--state-dir` is never pruned.

#### Retention

The global state directory is not cleaned when a repo is cleaned, so `latere agents review` runs a conservative retention pass before each run. Under a repo's `<repo-key>/sessions/` it keeps the **50 most recent** sessions and deletes any session older than **30 days**. Retention only touches the auto-resolved global directory; an explicit `--state-dir` is left untouched.

### How it works

1. Resolves the Claude Code session to fork (newest transcript under `--dir`, or `--session`).
2. Computes the working-tree diff and skips if trivial.
3. Forks the session as a proposer (local `claude`), and runs read-only critics in the Latere agent, whose model calls go to the Latere API with your model key.
4. Runs the debate to a steady state and prints a summary, writing per-fork artifacts under the review-log location above.

The critic model is billed to your current context; the console's Billing section shows the spend.

## Settings

| Setting | Purpose |
|---------|---------|
| `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` | Model credentials for `latere agents run`, in the order above. |
| `LATERE_AGENT_PROVIDER_FILE` | Where `latere agents provider` keeps the choice. |

[configuration.md](configuration.md) lists the files `latere agents provider`
keeps.
