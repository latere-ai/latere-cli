---
title: "`latere models`: the CLI's model commands over the platform origin, and the `latere lux` namespace retired"
status: implemented
depends_on:
  - specs/005-lux-env-redesign.md
  - specs/006-model-key.md
  - "CROSS-REPO: latere-ai/specs infrastructure/platform/ps-09-lux-cutover.md, 'What is dropped': the `latere lux` namespace, BYOK, the hosted plane"
  - "CROSS-REPO: platform specs/90-models-at-the-origin.md, the core at https://api.latere.ai/v1/models"
affects:
  - internal/commands/lux.go (becomes internal/commands/models.go; the hosted-plane commands leave)
  - internal/commands/lux_key.go (becomes internal/commands/models_key.go; the key is the only credential, and the actor-token path to the hosted plane leaves)
  - internal/modelkey/ensure.go (a key handed in through LATERE_MODEL_KEY needs no login)
  - internal/tunnel/ (leaves with `lux serve`; the sandbox tunnel keeps its machine id in internal/commands/topos_sandbox_serve.go)
  - internal/commands/review.go, topos_provider.go, topos_local_model.go, topos_local_tui.go (their model calls go to the origin with the model key)
  - internal/commands/root.go (the `models` command; `lux` is gone)
  - cmd/latere/*_e2e_test.go (the model e2e tests run against a stub core; the auth e2e tests run a product command other than `lux`)
  - .lateregate.yaml (the identity block no longer names the hosted plane's audience)
  - docs/lux.md (becomes docs/models.md), docs/review.md, docs/topos.md, docs/configuration.md, docs/login-and-tokens.md
  - README.md, CONTRIBUTING.md, CHANGELOG.md
created: 2026-09-25
updated: 2026-09-25
author: changkun
---

# `latere models` over the platform origin

On 2026-09-24 the Lux core took over the model endpoints at
`https://api.latere.ai/v1/models`, and the hosted plane at `lux.latere.ai`
is deleted in ps-09 step 5. The CLI's `latere lux` commands still default
to the hosted plane, and spec 006 left "flipping the default base URL and
porting `lux models`, `usage`, `access` and `serve`" to this change. ps-09
drops the `latere lux` namespace outright: the model commands are
`latere models`, over the origin, with the model key of spec 006.

## Design

| Command | What it does |
|---|---|
| `latere models` / `latere models list` | lists the Models the model key reaches, from the core's `GET /v1/models/openai/v1/models` with the key, by name; the door's list carries no price, so the command says prices are in the console's Models section |
| `latere models env [--provider openai\|anthropic\|gemini] [--raw]` | prints the exports a model SDK reads: the door's base URL under `https://api.latere.ai/v1/models` and the model key, as `lux env` did (spec 005's output shape: exports on stdout, the key's provenance on stderr, `--raw` for the bare key) |
| `latere models invoke --model <name> "<prompt>"` | one chat through the OpenAI door with the key, as `lux invoke` did |
| `latere models key` / `latere models key revoke` | shows the key's name, context and expiry, and revokes it, as `lux key` did |

`models env` exports, per `--provider` (default `openai`):

| `--provider` | Base URL variable, value | Key variable |
|---|---|---|
| `openai` | `OPENAI_BASE_URL`, `<base>/openai/v1` | `OPENAI_API_KEY` |
| `anthropic` | `ANTHROPIC_BASE_URL`, `<base>/anthropic` | `ANTHROPIC_API_KEY` (the core takes the key in `x-api-key` as in `Authorization`, and the platform's getting-started guide names this variable) |
| `gemini` | `GOOGLE_GEMINI_BASE_URL`, `<base>/gemini` | `GEMINI_API_KEY` (the base URL variable is the Gemini JavaScript SDK's and the Gemini CLI's; the Python SDK takes the base URL in code) |

`latere review`'s critics and the local Topos model path call models the
same way: through the core's door at the origin, with the model key, and
with a model named as the catalog names it (`anthropic/claude-sonnet-4.6`).
Their `--lux-url` flag becomes `--models-url`.

The base URL defaults to `https://api.latere.ai/v1/models`; `LATERE_MODELS_URL`
or `--models-url` overrides it. The key of spec 006 is the only credential:
it is created at auth on first use in the current context, kept in the
system keychain, and presented to the core as the bearer. A key handed in
through `LATERE_MODEL_KEY` is presented as is and needs no login, which is
what a CI job that is handed a key has.

Removed, with the reason:

| Removed | Reason |
|---|---|
| `latere lux` and every subcommand under it | ps-09 drops the namespace; no alias, no compatibility window |
| `lux access show/set/clear` | BYOK is over (ps-09 decision 2) |
| `lux rates`, `lux providers` (hidden) | the hosted plane's `/lux/v1/rates` and `/lux/v1/providers`; a Model's price is in the console's Models section |
| `lux usage` | the hosted plane's usage API; a person's spend is the console's Billing section |
| `lux token` | printed an actor token for the hosted plane; the key is the credential, and `models env --raw` prints it |
| `lux serve` and `internal/tunnel` | the reverse tunnel to the hosted plane; the core's tunnel is off at the origin |
| `LUX_API_URL`, `--lux-url`, the `lux.latere.ai` audience | the hosted plane's address and audience |
| `LATERE_LUX_TOKEN`, `--token` on `lux` and `review` | a bearer handed to the hosted plane in place of a minted one; the core takes a key, handed in through `LATERE_MODEL_KEY` |

### How review and the local Topos agent reach the door

Both called the hosted plane's native dialect, `POST /lux/v1/generate`,
through the Topos runtime (`topos.ModelLux`, luxsdk's gateway client). The
Topos runtime also has a direct mode, `topos.ModelDirect` over
`luxsdk.NewDirect`, which translates the same requests to a provider
dialect on the caller's machine and posts them to a base URL with a bearer.
With the provider `openai` and the base `<base>/openai`, it posts Chat
Completions (the Responses API for OpenAI's reasoning models) to the core's
OpenAI door, which translates again for a Model of another provider. So
the change needs nothing from another repository: `review` sets
`ModelDirect` on its critic factory, and `topos --local` builds the direct
caller itself.

The Topos runtime presents the bearer and has no part in the key's
first-use wait or its replacement (spec 006). So the session's bearer
source lists the models with the key before the first model call, which
applies both, and answers the key the core accepted from then on. `review`
calls it before the proposer's first round, so a missing login or a
refused key fails before any spend.

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | `latere models`, `list`, `env`, `invoke`, `key` and `key revoke` work against a stub core at a `/v1/models` base with the model key | `internal/commands` tests with a stub core and an in-memory keychain |
| 2 | The default base URL is `https://api.latere.ai/v1/models`, overridden by `LATERE_MODELS_URL` and `--models-url` | `internal/commands` test |
| 3 | `latere lux` is an unknown command, and no source file names `lux.latere.ai`, `/lux/v1/` or `LUX_API_URL` | a test over the command tree; a grep test over the tree |
| 4 | `latere review`'s critics and the local Topos model path call a stub core's door with the model key and a catalog model name | `internal/commands` review and topos tests |
| 5 | `docs/models.md` describes the commands; `docs/lux.md` is gone; the README names `latere models` | docs tests where the repository has them |

## Outcome

Built on 2026-09-25, not released. Every criterion has a passing test:

| # | Tests |
|---|---|
| 1 | `internal/commands/models_test.go`: `TestModelsListPrintsNames` (`models` and `models list`, names and the prices note), `TestModelsListEmpty`, `TestModelsEnvExportsEachDoor` (each `--provider`), `TestModelsInvokeCallsTheOpenAIDoor`, `TestModelsErrorsSayWhatToDo`; `internal/commands/models_key_test.go`: `TestModelsEnvCreatesTheModelKey`, `TestModelsKeyShowAndRevoke`, `TestHandedKeyNeedsNoLogin`, and spec 006's criteria over the new commands (`TestInvokeRetriesAFreshKey`, `TestInvokeReplacesARefusedKey`, `TestModelKeyRefusals`, `TestModelKeyFollowsTheContext`, `TestLogoutForgetsTheModelKeys`); the built binary in `cmd/latere/models_env_e2e_test.go`, `models_invoke_e2e_test.go` and `models_list_e2e_test.go` |
| 2 | `TestResolveModelsURL`, `TestModelsURLReachesTheCommands` |
| 3 | `TestModelsReplacesLux`, `TestLuxIsAnUnknownCommandE2E`, `TestNoSourceNamesTheHostedPlane` (every Go file, the README, CONTRIBUTING.md and `docs/`) |
| 4 | `TestReviewCriticCallsTheDoor` (a critic round streams `anthropic/claude-sonnet-4.6` from the stub's OpenAI door with the key), `TestLocalModelCallsTheDoor`, `TestModelKeySourceWaitsThenAnswersTheAcceptedKey` |
| 5 | `TestDocsDescribeModels` |

Where the build differs from the draft:

- The core's OpenAI door lists Models by name only (`bridge.Model` carries
  a name and an owner), so `models list` prints names and says on stderr
  that prices are in the console's Models section, rather than a price per
  million tokens.
- `models env` keeps spec 005's `--raw`, which takes over `lux token`'s
  job, and exports `ANTHROPIC_API_KEY` rather than `ANTHROPIC_AUTH_TOKEN`:
  the hosted plane ignored `x-api-key`, the core does not.
- `LATERE_MODEL_KEY` no longer needs a saved login. Spec 006 read the login
  before the handed key, so a CI job with a key and no login could not use it.
- `LATERE_LUX_TOKEN` and `review --token` are removed along with `--lux-url`,
  since the core takes no bearer but a key.
- `latere topos --local` defaults to `anthropic/claude-sonnet-4.6`, the
  catalog name confirmed at the origin, where it asked the hosted plane for
  `claude-opus-4-8`; `--model` still picks another.
- The `/model` picker of `latere topos --local` lists every Model the key
  reaches, where it listed the hosted plane's Anthropic Models: the OpenAI
  door reaches a Model of any provider.
- The auth e2e tests that used `lux env --raw` as the command that refreshes
  a login and mints with it now run `drive ls` (their failure paths need a
  command that reports failure; the git credential helper is silent by
  protocol) or `git-credential get`.
- The sandbox tunnel of `latere topos serve-sandbox` used `internal/tunnel`
  for its fallback machine id; that function moved beside it, keeping the
  `tunnel-node-id` file.
