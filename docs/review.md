# Review

`latere review` runs an adversarial review of your most recent Claude Code session: it forks the session as a *proposer* that defends the change, runs *critics* against the working-tree diff, and surfaces the attacks that survive. Run `latere login` first (see the [main README](../README.md#sign-in)).

The proposer runs locally through your own `claude` CLI for full fidelity (it forks your real session with `claude --resume <id> --fork-session`), while the critics call their model through the Latere API with your [model key](models.md#your-model-key), so critic model cost is drawn from your current context and no provider key is needed locally.

## Prerequisites

- `latere login` (the critics call models with your model key, which the CLI creates on first use).
- The [`claude`](https://code.claude.com/docs) CLI installed and authenticated (the proposer forks your real Claude Code session).
- A git repository with a recent Claude Code session in it.

## Quickstart

From inside a repo where you just finished a Claude Code session:

```sh
latere review
```

That reviews the most recent session under the working directory. To pick a specific session or run a deeper debate:

```sh
latere review --session 4f3c2b1a --forks 3 --max-rounds 6
```

`review` looks at the working-tree diff (`HEAD` vs the tree); when the tree is clean (Claude already committed) it falls back to `HEAD~1..HEAD`. A trivial diff is skipped.

## Exit codes

`latere review` carries the review verdict in its exit code, so it can gate a script or hook:

```sh
latere review && git push     # push only if the review is clean
```

| Code | Meaning |
|------|---------|
| `0` | Debate completed with no unresolved attacks |
| `2` | Debate completed but left unresolved attacks |
| `1` | Command error (not signed in, no session, the Latere API unreachable or refusing the key) |

## Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `--session` | most recent under `--dir` | Claude Code session ID to review |
| `--dir` | `.` | Working directory (git repo root and Claude session home) |
| `--state-dir` | `$XDG_STATE_HOME/latere/reviews/<repo-key>/` | Where to write review logs |
| `--forks` | `1` | Number of independent critic forks |
| `--max-rounds` | `4` | Per-fork debate-round cap |
| `--cost-cap` | `50000` | Soft token budget (proposer tokens; topos critics report no usage yet) |
| `--model` | `anthropic/claude-sonnet-4.6` | Critic model, named as `latere models` lists it |
| `--proposer-timeout` | `5m` | Per-round deadline for the proposer's claude call (large sessions may need more) |
| `--models-url` | `LATERE_MODELS_URL` or `https://api.latere.ai/v1/models` | Override the models base URL |
| `--auth-url` | derived from the models URL | Override the auth base URL |

## Review-log location

Review logs are transient state, so they are written to a user-global XDG state directory rather than into the repo you are reviewing:

```
$XDG_STATE_HOME/latere/reviews/<repo-key>/sessions/<session-id>/
  fallback: ~/.local/state/latere/reviews/<repo-key>/sessions/<session-id>/
```

`<repo-key>` is a stable, filesystem-safe key derived from the repository's git toplevel path (a readable slug plus a short hash), so each project's reviews accumulate under one findable folder and two repos with the same name never collide. Pass `--state-dir <path>` to write somewhere else for a one-off run; a user-chosen `--state-dir` is never pruned.

### Retention

The global state directory is not cleaned when a repo is cleaned, so `latere review` runs a conservative retention pass before each run. Under a repo's `<repo-key>/sessions/` it keeps the **50 most recent** sessions and deletes any session older than **30 days**. Retention only touches the auto-resolved global directory; an explicit `--state-dir` is left untouched.

## How it works

1. Resolves the Claude Code session to fork (newest transcript under `--dir`, or `--session`).
2. Computes the working-tree diff and skips if trivial.
3. Forks the session as a proposer (local `claude`), and runs read-only critics through [topos](https://github.com/latere-ai/topos), whose model calls go to the Latere API with your model key.
4. Runs the debate to a steady state and prints a summary, writing per-fork artifacts under the review-log location above.

The critic model is billed to your current context; the console's Billing section shows the spend.
