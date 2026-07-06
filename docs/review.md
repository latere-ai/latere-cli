# Agon

`latere agon` runs an adversarial review of your most recent Claude Code session: it forks the session as a *proposer* that defends the change, runs *critics* against the working-tree diff, and surfaces the attacks that survive. Run `latere auth login` first (see the [main README](../README.md#sign-in)).

The proposer runs locally through your own `claude` CLI for full fidelity (it forks your real session with `claude --resume <id> --fork-session`), while the critics run through [Lux](https://lux.latere.ai), so critic model cost is tracked on your Latere identity and no provider key is needed locally.

## Prerequisites

- `latere auth login` (grants the `llm.invoke` scope the critics need).
- The [`claude`](https://docs.claude.com/en/docs/claude-code) CLI installed and authenticated (the proposer forks your real Claude Code session).
- A git repository with a recent Claude Code session in it.

## Quickstart

From inside a repo where you just finished a Claude Code session:

```sh
latere agon
```

That reviews the most recent session under the working directory. To pick a specific session or run a deeper debate:

```sh
latere agon --session 4f3c2b1a --forks 3 --max-rounds 6
```

`agon` reviews the working-tree diff (`HEAD` vs the tree); when the tree is clean (Claude already committed) it falls back to `HEAD~1..HEAD`. A trivial diff is skipped.

## Exit codes

`latere agon` carries the review verdict in its exit code, so it can gate a script or hook:

```sh
latere agon && git push     # push only if the review is clean
```

| Code | Meaning |
|------|---------|
| `0` | Debate completed with no unresolved attacks |
| `2` | Debate completed but left unresolved attacks |
| `1` | Command error (not signed in, no session, Lux unreachable, expired token) |

## Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `--session` | most recent under `--dir` | Claude Code session ID to review |
| `--dir` | `.` | Working directory (git repo root and Claude session home) |
| `--state-dir` | same as `--dir` | Where to write `.agon/sessions/` |
| `--forks` | `1` | Number of independent critic forks |
| `--max-rounds` | `4` | Per-fork debate-round cap |
| `--cost-cap` | `50000` | Soft token budget (proposer tokens; topos critics report no usage yet) |
| `--model` | `claude-sonnet-4-6` | Critic model, routed through Lux |
| `--proposer-timeout` | `5m` | Per-round deadline for the proposer's claude call (large sessions may need more) |
| `--lux-url` | `LUX_API_URL` or `https://lux.latere.ai` | Override the Lux base URL |
| `--auth-url` | derived from the Lux URL | Override the auth base URL |
| `--token` | minted from your login | Present this bearer to Lux instead (e.g. a sandbox token) |

## How it works

1. Resolves the Claude Code session to fork (newest transcript under `--dir`, or `--session`).
2. Computes the working-tree diff and skips if trivial.
3. Forks the session as a proposer (local `claude`), and runs read-only critics through [topos](https://github.com/latere-ai/topos) with model calls routed via Lux on your identity.
4. Runs the debate to a steady state and prints a summary, writing per-fork artifacts under `.agon/sessions/`.

The critic model bills against your Latere account; run `latere lux usage` to see spend.
