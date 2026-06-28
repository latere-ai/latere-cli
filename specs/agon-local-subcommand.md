---
title: latere agon local subcommand
status: implemented
depends_on:
  - latere.ai/x/agon pkg/adversarial (spec 37)
  - latere.ai/x/agon pkg/adversarial/input (spec 40)
  - latere.ai/x/agon pkg/adversarial/topos (spec 39)
affects:
  - internal/commands/agon.go
  - internal/commands/root.go
  - go.mod
  - go.sum
effort: medium
created: 2026-06-28
updated: 2026-06-28
author: changkun
shipped_in: latere.ai/x/agon v0.1.3
dispatched_task_id: null
---

# latere agon local subcommand

## Problem

`agon` ships as a standalone binary (`cmd/agon`) that must be installed
separately, authenticates critics through the developer's own Claude/Codex CLI
credentials, and has no integration with the shared Latere identity or model
gateway. Running it requires `agon`, `claude`, and `codex` all installed and
separately authenticated.

The `latere` CLI already carries the user's Latere bearer token and has Lux
(the Latere model gateway) as its model routing layer. The agon engine was
restructured in specs 37–40 to be importable: `pkg/adversarial` is the engine
spine, `pkg/adversarial/input` reads Claude Code sessions and computes diffs,
`pkg/adversarial/claude` wraps the proposer CLI, and `pkg/adversarial/topos`
runs critics through any topos-capable model backend. These packages exist and
compile today.

## Goal

Add `latere agon` as a Cobra subcommand in latere-cli. It runs the full
adversarial review locally, on the developer machine, against the most recent
Claude Code session in the working directory:

- **Proposer**: `pkg/adversarial/claude.NewProposer`, shelling to
  `claude --resume <id> --fork-session`. Full-fidelity: the real session
  transcript, harness context, and working tree are reconstituted by the claude
  CLI. No change from `cmd/agon`'s proposer behavior.
- **Critics**: `pkg/adversarial/topos.NewCriticFactory` with
  `Config.Model.Kind = ModelLux`. Model calls are routed through Lux using the
  stored bearer token from `~/.config/latere/token.json`, so cost is tracked
  against the user's Latere account and no provider key is needed locally.
- **Session/diff resolution**: `pkg/adversarial/input.ReadTranscript` and
  `input.Compute` locate the session and compute the diff, just as `cmd/agon`
  does.

The standalone `cmd/agon` binary is sunset once `latere agon` passes an
equivalent real-e2e smoke (see "Sunset" below).

## Design

### New file: `internal/commands/agon.go`

`newAgonCmd()` returns a `*cobra.Command` registered as `latere agon`. The
command is entirely local (no HTTP calls to the Latere backend except via
Lux inside the topos runner).

**Flags** (mirror `cmd/agon` defaults):

| Flag | Default | Purpose |
|---|---|---|
| `--session` | `""` | Claude Code session ID to review; empty = most recent under `--dir` |
| `--dir` | `"."` | Working directory (git repo root and Claude session home) |
| `--state-dir` | `""` | Where to write `.agon/sessions/`; empty = same as dir |
| `--forks` | `1` | Critic fork count |
| `--max-rounds` | `4` | Per-fork internal-round cap |
| `--cost-cap` | `50000` | Soft token budget (proposer tokens; topos critics report no usage yet) |
| `--model` | `"claude-sonnet-4-6"` | Critic model routed through Lux |
| `--lux-url` | `""` | Lux base URL; empty = `LUX_API_URL` or `https://lux.latere.ai` |
| `--auth-url` | `""` | Auth base URL; empty = derived from the Lux URL |
| `--token` | `""` | Present this bearer to Lux instead of minting one (e.g. a sandbox token) |

**RunE sketch** (pseudocode, reflecting what shipped):

```
// 1. Identity bearer up front: validate Lux sign-in + preflight scope.
//    agon.go lives in package commands, so it calls lux.go's helper directly.
bearerFn = func(ctx) (string, error) {
    return luxIdentityBearer(ctx, tokenFlag, luxURLFlag, authURLFlag)
}
bearer, err = bearerFn(ctx)                       // errors if not signed in
ensureLuxScope(bearer, ["llm.invoke"], "...")     // friendly 403 preflight

// 2. Resolve the session to fork (proposer needs a real --resume target).
sessionID = sessionFlag
if sessionID == "" {
    sessionID, transcriptPath = mostRecentSession(home, cwd)  // local helper
} else {
    transcriptPath = input.LocateTranscript(home, cwd, sessionID, "")
}

// 3. Diff + gate (mirrors cmd/agon's clean-tree HEAD~1 fallback).
diff = input.Compute(ctx, input.DiffSpec{From:"HEAD", To:".", Cwd:cwd})
if diff.ChangedLines == 0 { try HEAD~1..HEAD fallback }
if input.Trivial(diff, 0) { print "no substantive diff"; return nil }

taskCtx = input.ReadTranscript(transcriptPath).FirstUser   // best-effort

proposer  = claude.NewProposer(sessionID, cwd)             // local claude auth
criticFac = topos.NewCriticFactory(topos.Config{
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

**Token (corrected from the original draft).** The bearer handed to Lux is NOT
the Cella `token.json` (`api.LoadToken`). It is the retained auth.latere.ai
identity token (`auth-token.json`), which Lux accepts directly, obtained via
lux.go's `luxIdentityBearer` (which also honors `--token` / `LATERE_LUX_TOKEN` /
the sandbox token, and refreshes an expired auth token). `BearerSource` is the
*identity* bearer, not a 5-minute actor token, because a debate makes many model
calls and can outlive a short-lived token; passing the closure (not a string)
means topos re-fetches and refreshes on each call. The proposer is unaffected:
it forks the local Claude session under the developer's own claude auth.

**Most-recent discovery lives here, not in the input package.** No importable
helper resolves "newest session" (`cmd/agon` always receives its session ID
from the stop hook). `mostRecentSession(home, cwd)` scans
`~/.claude/projects/<input.EncodeCwd(cwd)>/*.jsonl` and picks the newest by
mtime. (Edge: invoked from inside a live Claude session, "most recent" is the
current session being forked, correct for the post-hoc terminal case but
surprising from within a session.)

### `internal/commands/root.go`

One line addition after `newLuxCmd()`:

```go
root.AddCommand(newAgonCmd())
```

### `go.mod`

```
require latere.ai/x/agon v0.1.3
require latere.ai/x/topos v0.0.5  // direct: agon.go references xtopos.ModelOptions
```

Both are published on the public Go proxy (same pattern as `latere.ai/x/topos`
in agon's own `go.mod`). No replace directive. Adding the agon dependency also
pulled cobra forward to v1.10.2 transitively. Shipped at agon tag `v0.1.3`
(spec 39 + spec 40 land between `v0.1.2` and `v0.1.3`).

## Error handling

- Not signed in for Lux (`luxIdentityBearer` error): the helper already returns
  `"not signed in for Lux; run latere auth login ..."`; surfaced as-is.
- Missing `llm.invoke` scope: `ensureLuxScope` returns a friendly, specific
  error before any model call is spent (skipped for opaque non-JWT tokens).
- Diff trivial (`input.Trivial(diff, 0)` → true): print `"no substantive diff to review"` and return nil (same fast path as `cmd/agon`).
- No session found: `mostRecentSession` returns an actionable error (run a
  session here, or pass `--session <id>`); the `--session` path wraps
  `input.ErrTranscriptNotFound` with the same hint.
- Lux invocation failure (topos critic returns non-nil error): surfaced as-is;
  it carries the HTTP status.

## Testing strategy

The `cmd/agon` suite is not duplicated here; the engine logic is tested in
`latere.ai/x/agon`. Shipped tests in `internal/commands/agon_test.go`:

- **`TestAgonFlagDefaults`**: builds the command and asserts each flag's default
  matches the table above. No I/O.
- **`TestMostRecentSessionPicksNewest`**: builds a fake
  `~/.claude/projects/<encoded-cwd>/` tree and asserts the newest `.jsonl` by
  mtime wins, its session ID is the basename, and non-`.jsonl` files are ignored.
- **`TestMostRecentSessionNoSessions`**: a missing or transcript-empty project
  dir returns a clear, actionable error.

The auth gate and trivial-diff fast path are exercised by the live smoke rather
than unit-mocked: both depend on real I/O (a signed-in bearer, a git tree) that
is cheaper to cover end-to-end than to fake. A real e2e smoke equivalent to spec
34 (`34-real-claude-end-to-end-smoke.md`) is the acceptance gate for sunsetting
`cmd/agon` (see below).

## Lux routing detail

topos's `ModelLux` kind routes the Anthropic-wire request through Lux. `agon.go`
sets `BearerSource` to `luxIdentityBearer`, the same identity-bearer path
`latere lux env` / `lux serve` use; it returns a passthrough token when one is
configured, otherwise the refreshed auth.latere.ai identity token, which Lux
accepts directly. `BaseURL` is `resolveLuxURL(--lux-url) + "/anthropic"`; the
topos anthropic adapter appends `/v1/messages`, so requests land on Lux's
`/anthropic/v1/messages` route as `Authorization: Bearer`. The provider key
stays in Lux, never in the CLI.

For local Lux development (`LUX_STATELESS=1` + personal provider key), set
`--lux-url http://localhost:<port>` (no `/anthropic` suffix; the command appends
it), the same override pattern `latere lux` uses via `LUX_API_URL`.

> Unverified: the topos→Lux→anthropic path has only run against a scripted
> brain. Token, base URL, and wire format get their first live exercise in the
> smoke below; run one real critic round before relying on it.

## Sunset plan for `cmd/agon`

`cmd/agon` stays until:

1. `latere agon` passes a real end-to-end smoke on a known session (proposer
   forks the Claude session, at least one topos critic completes a round via
   Lux, summary is printed). This is the `latere agon` equivalent of spec 34.
2. Install docs updated: `latere auth login` + `latere agon` replaces `agon`.

After both gates: delete `cmd/agon/`, remove the `agon` binary from the release
workflow, and mark spec 39's Phase 2 (external-importer wiring) as complete via
this subcommand.

## Non-goals

- Hosted Verifier service (`agon.latere.ai`). That is the cloud track; this
  spec is the local track only.
- HTTP client wrapper for the hosted service. Also cloud track.
- Changing the debate protocol, aspect prompts, or output format. Backend swap +
  CLI integration only.
- Moving the proposer to topos. See spec 39 "Why critic-only".
- Auto-detecting the session home on non-macOS paths. `latere agon` targets the
  same developer context as `cmd/agon`; the Claude session home is `~/.claude`.
