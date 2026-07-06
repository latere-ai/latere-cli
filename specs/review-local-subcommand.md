---
title: latere review local subcommand
status: implemented
depends_on:
  - latere.ai/x/topos/adversarial
  - latere.ai/x/topos/adversarial/input
  - latere.ai/x/topos/adversarial/critic
affects:
  - internal/commands/review.go
  - internal/commands/root.go
  - internal/reviews/reviews.go
  - go.mod
  - go.sum
effort: medium
created: 2026-06-28
updated: 2026-07-07
author: changkun
dispatched_task_id: null
---

# latere review local subcommand

## Goal

`latere review` runs a full adversarial review locally, on the developer
machine, against the most recent Claude Code session in the working directory:

- **Proposer**: `topos/adversarial/claude.NewProposer`, shelling to
  `claude --resume <id> --fork-session`. Full-fidelity: the real session
  transcript, harness context, and working tree are reconstituted by the claude
  CLI.
- **Critics**: `topos/adversarial/critic.NewCriticFactory` with
  `Config.Model.Kind = ModelLux`. Model calls are routed through Lux using the
  retained Latere identity bearer, so cost is tracked against the user's Latere
  account and no provider key is needed locally.
- **Session/diff resolution**: `topos/adversarial/input.ReadTranscript` and
  `input.Compute` locate the session and compute the diff.

The command talks to no Latere backend except via Lux (inside the topos critic
runtime).

## Design

### `internal/commands/review.go`

`newReviewCmd()` returns a `*cobra.Command` registered as `latere review`.

**Flags:**

| Flag | Default | Purpose |
|---|---|---|
| `--session` | `""` | Claude Code session ID to review; empty = most recent under `--dir` |
| `--dir` | `"."` | Working directory (git repo root and Claude session home) |
| `--state-dir` | `""` | Where to write review logs; empty = `$XDG_STATE_HOME/latere/reviews/<repo-key>/` |
| `--forks` | `1` | Critic fork count |
| `--max-rounds` | `4` | Per-fork internal-round cap |
| `--cost-cap` | `50000` | Soft token budget (proposer tokens; topos critics report no usage yet) |
| `--model` | `"claude-sonnet-4-6"` | Critic model routed through Lux |
| `--proposer-timeout` | `5m` | Per-round deadline for the proposer's claude call |
| `--lux-url` | `""` | Lux base URL; empty = `LUX_API_URL` or `https://lux.latere.ai` |
| `--auth-url` | `""` | Auth base URL; empty = derived from the Lux URL |
| `--token` | `""` | Present this bearer to Lux instead of minting one (e.g. a sandbox token) |

**RunE sketch:**

```
// 1. Resolve the review-log location. Default to the user-global XDG state
//    dir keyed by the reviewed repo; run a retention pass on it.
stateDir = stateDirFlag
if stateDir == "" {
    stateDir = reviews.Dir(gitToplevel(cwd))   // $XDG_STATE_HOME/latere/reviews/<repo-key>
    reviews.Prune(stateDir)
}

// 2. Identity bearer up front: validate Lux sign-in + preflight scope.
bearerFn = func(ctx) (string, error) {
    return luxIdentityBearer(ctx, tokenFlag, luxURLFlag, authURLFlag)
}
bearer, err = bearerFn(ctx)                       // errors if not signed in
ensureLuxScope(bearer, ["llm.invoke"], "...")     // friendly 403 preflight
ensureBearerFresh(bearer)                          // expiry fast-fail

// 3. Resolve the session to fork (proposer needs a real --resume target).
sessionID = sessionFlag
if sessionID == "" {
    sessionID, transcriptPath = mostRecentSession(home, cwd)
} else {
    transcriptPath = input.LocateTranscript(home, cwd, sessionID, "")
}

// 4. Diff + gate (clean-tree HEAD~1 fallback).
diff = input.Compute(ctx, input.DiffSpec{From:"HEAD", To:".", Cwd:cwd})
if diff.ChangedLines == 0 { try HEAD~1..HEAD fallback }
if input.Trivial(diff, 0) { print "no substantive diff"; return nil }

taskCtx = input.ReadTranscript(transcriptPath).FirstUser   // best-effort

proposer  = claude.NewProposer(sessionID, cwd, claude.WithProposerDeadline(...))
criticFac = critic.NewCriticFactory(critic.Config{
    Model: xtopos.ModelOptions{
        Kind:         xtopos.ModelLux,
        Provider:     "anthropic",
        Model:        modelFlag,
        BaseURL:      resolveLuxURL(luxURLFlag) + "/anthropic",
        BearerSource: bearerFn,                   // re-fetched/refreshed per call
    },
})

summary = (&adversarial.Engine{ StateDir, Cwd, ForkCount, Proposer:proposer,
    NewCritic:criticFac, MaxRounds, CostCap, TaskContext:taskCtx,
    DiffPatch:diff.Patch }).Run(ctx)
print summary
```

**Token.** The bearer handed to Lux is the retained auth.latere.ai identity
token, which Lux accepts directly, obtained via lux.go's `luxIdentityBearer`
(which also honors `--token` / `LATERE_LUX_TOKEN` / the sandbox token, and
refreshes an expired auth token). `BearerSource` is the *identity* bearer, not a
short-lived actor token, because a debate makes many model calls and can outlive
one; passing the closure (not a string) means topos re-fetches and refreshes on
each call. The proposer is unaffected: it forks the local Claude session under
the developer's own claude auth.

**Most-recent discovery lives here, not in the input package.** No importable
helper resolves "newest session". `mostRecentSession(home, cwd)` scans
`~/.claude/projects/<input.EncodeCwd(cwd)>/*.jsonl` and picks the newest by
mtime.

### `internal/reviews/reviews.go`

`reviews.Dir(repoRoot)` resolves `$XDG_STATE_HOME/latere/reviews/<repo-key>`
(fallback `~/.local/state/latere/reviews/<repo-key>`). `<repo-key>` is a
filesystem-safe, stable, collision-resistant key: a readable slug of the repo
basename plus a short hash of the full git toplevel path. The engine writes
`sessions/<id>/` under this path.

`reviews.Prune(dir)` enforces retention under `dir/sessions/`: keep the 50
newest sessions by mtime, delete any older than 30 days. Best-effort and a
no-op when the sessions dir does not yet exist, so it is safe before every run.
Retention runs only for the auto-resolved global dir; an explicit `--state-dir`
is left untouched.

### `internal/commands/root.go`

```go
root.AddCommand(newReviewCmd())
```

## Review-log location

Review logs are transient state and must not land in the reviewed working tree.
The default location is the user-global XDG state dir, namespaced by repo:

```
$XDG_STATE_HOME/latere/reviews/<repo-key>/sessions/<session-id>/
  fallback: ~/.local/state/latere/reviews/<repo-key>/sessions/<session-id>/
```

`--state-dir` overrides for one-off runs and is exempt from retention.

## Error handling

- Not signed in for Lux: `luxIdentityBearer` returns
  `"not signed in for Lux; run latere auth login ..."`, surfaced as-is.
- Missing `llm.invoke` scope: `ensureLuxScope` returns a friendly, specific
  error before any model call is spent (skipped for opaque non-JWT tokens).
- Expired identity token: `ensureBearerFresh` fast-fails with a re-login hint.
- Diff trivial: print `"no substantive diff to review"` and return nil.
- No session found: `mostRecentSession` returns an actionable error; the
  `--session` path wraps `input.ErrTranscriptNotFound` with the same hint.
- Lux invocation failure: surfaced as-is; it carries the HTTP status.

## Testing strategy

Shipped tests in `internal/commands/review_test.go`:

- **`TestReviewFlagDefaults`**: asserts each flag's default matches the table.
- **`TestMostRecentSessionPicksNewest`** / **`TestMostRecentSessionNoSessions`**:
  cover the newest-transcript discovery and its actionable no-session error.

Shipped tests in `internal/reviews/reviews_test.go`:

- **`TestDirUsesXDGStateHome`** / **`TestDirFallsBackToLocalState`**: cover both
  resolution branches.
- **`TestRepoKeyStableAndDistinct`**: the key is stable, readable, and distinct
  for same-basename repos.
- **`TestPruneKeepsNewestAndDropsOldAndAged`** / **`TestPruneNoSessionsDirIsNoop`**:
  cover the retention caps and the safe no-op.

The auth gate and trivial-diff fast path are exercised by a live smoke rather
than unit-mocked: both depend on real I/O (a signed-in bearer, a git tree) that
is cheaper to cover end-to-end than to fake.

## Lux routing detail

topos's `ModelLux` kind routes the Anthropic-wire request through Lux. The
command sets `BearerSource` to `luxIdentityBearer`, the same identity-bearer
path `latere lux env` / `lux serve` use; it returns a passthrough token when one
is configured, otherwise the refreshed auth.latere.ai identity token, which Lux
accepts directly. `BaseURL` is `resolveLuxURL(--lux-url) + "/anthropic"`; the
topos anthropic adapter appends `/v1/messages`, so requests land on Lux's
`/anthropic/v1/messages` route as `Authorization: Bearer`. The provider key
stays in Lux, never in the CLI.

For local Lux development (`LUX_STATELESS=1` + personal provider key), set
`--lux-url http://localhost:<port>` (no `/anthropic` suffix; the command appends
it), the same override pattern `latere lux` uses via `LUX_API_URL`.

## Non-goals

- Hosted Verifier service. That is the cloud track; this spec is local only.
- Changing the debate protocol, aspect prompts, or output format.
- Moving the proposer to topos (see topos's "Why critic-only").
- Auto-detecting the session home on non-macOS paths; the Claude session home
  is `~/.claude`.
