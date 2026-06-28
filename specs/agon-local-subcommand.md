---
title: latere agon local subcommand
status: drafted
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
| `--session` | `""` | Claude Code session ID to review; empty = most recent in dir |
| `--dir` | `"."` | Working directory (git repo root and Claude session home) |
| `--state-dir` | `""` | Where to write `.agon/sessions/`; empty = same as dir |
| `--forks` | `1` | Critic fork count |
| `--max-rounds` | `4` | Per-fork internal-round cap |
| `--cost-cap` | `50000` | Soft token budget across all forks |
| `--model` | `"claude-sonnet-4-6"` | Critic model routed through Lux |
| `--lux-url` | `"https://lux.latere.ai/anthropic"` | Lux base URL; override for local luxd dev |

**RunE sketch** (pseudocode, not literal implementation):

```
token, err = api.LoadToken("")                    // shared bearer
tr, err    = input.ReadTranscript(
               input.LocateTranscript(home, cwd, sessionFlag, ""))
diff, err  = input.Compute(ctx, input.DiffSpec{From:"HEAD", To:".", Cwd:cwd})
if input.Trivial(diff, 0) { print "diff too small"; return }

proposer  = claude.NewProposer(tr.SessionID, cwd)
criticFac = topos.NewCriticFactory(topos.Config{
    Model: xtopos.ModelOptions{
        Kind:         xtopos.ModelLux,
        Provider:     "anthropic",
        Model:        modelFlag,
        BaseURL:      luxURLFlag,
        BearerSource: func(ctx) (string, error) { return token.AccessToken, nil },
    },
})

summary, err = (&adversarial.Engine{
    StateDir:    stateDir,
    Cwd:         cwd,
    ForkCount:   forksFlag,
    Proposer:    proposer,
    NewCritic:   criticFac,
    MaxRounds:   maxRoundsFlag,
    CostCap:     costCapFlag,
    TaskContext:  extractedFirstUser,
    DiffPatch:   diff.Patch,
}).Run(ctx)

print summary
```

The token is passed as a `BearerSource` closure (not a static `APIKey`) so that
a token refresh, if added later, flows through without changing this code.

### `internal/commands/root.go`

One line addition after `newLuxCmd()`:

```go
root.AddCommand(newAgonCmd())
```

### `go.mod`

```
require latere.ai/x/agon <current-tagged-version>
```

`latere.ai/x/agon` is published on the public Go proxy (same pattern as
`latere.ai/x/topos v0.0.5` in agon's own `go.mod`). No replace directive.
The indirect topos dependency is already satisfied transitively; `go mod tidy`
promotes `latere.ai/x/topos` as indirect if it is not already direct.

## Error handling

- No bearer token (`api.ErrNoToken`): print `"not logged in; run latere auth login"` and return.
- Diff trivial (`input.Trivial(diff, 0)` → true): print `"no substantive diff to review"` and return nil (same fast path as `cmd/agon`).
- No session found (`input.ErrTranscriptNotFound`): print a clear message suggesting `--session <id>`.
- Lux auth failure (topos critic returns non-nil error): surface the underlying error as-is; it already carries the HTTP status.

## Testing strategy

The `cmd/agon` suite is not duplicated here; the engine logic is tested in
`latere.ai/x/agon`. Tests in `internal/commands/` should cover:

- **Flag parsing unit test**: `TestAgonFlagsDefaults` — build the command, parse
  known flags, assert defaults match the table above. No I/O.
- **Auth gate test**: command run with no token file → returns `api.ErrNoToken`
  (already covered by the pattern in `internal/commands/auth_test.go`).
- **Trivial-diff fast path**: supply a near-empty diff mock; assert the command
  returns nil and prints the "no substantive diff" message without calling the
  engine. Can be tested by injecting a small `DiffSpec` against an empty temp
  git repo.

A real e2e smoke equivalent to spec 34 (`34-real-claude-end-to-end-smoke.md`)
is the acceptance gate for sunsetting `cmd/agon` (see below).

## Lux routing detail

topos's `ModelLux` kind routes the Anthropic-wire request through Lux. The CLI
sets `BearerSource` to return the stored bearer from `token.json` on each call.
Lux validates the bearer against the user's Latere identity and forwards the
request to the configured provider; the API key stays in Lux, not in the CLI.

For local Lux development (`LUX_STATELESS=1` + personal provider key), set
`--lux-url http://localhost:<port>/anthropic` — the same override pattern the
`latere lux` command uses via `SANDBOX_API_URL`.

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
