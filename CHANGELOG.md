# Changelog

Every tag has a section here, and the section is the body of the GitHub
release. A tag without one is refused at the pre-push and fails the release
workflow. Write under `Unreleased` as work lands; `lateregate release vX.Y.Z`
turns that into the tag's section, commits, tags and pushes.

A section says what changed for whoever uses the release, not what was
committed: the commit log already holds that.

## Unreleased

## v0.13.0 - 2026-09-26

### Changed

- `latere cella` runs on the Cella API at
  `https://api.latere.ai/v1/environments`, which replaces the retired
  hosted sandbox service at `cella.latere.ai`. `LATERE_CELLA_URL` or
  `--api-url` overrides the base URL, which includes its
  `/v1/environments` path. Commands present a token minted for the audience
  `cella`.

- A create answers as soon as the sandbox is recorded, usually `Pending`.
  `apply --wait[=DURATION]` holds the answer until the sandbox runs or
  fails, and a sandbox that fails to start exits 1 with its reason.
  Manifests name `apiVersion: cella.latere.ai/v1beta1` and an image from
  the catalog, `base` or `gui`. A manifest that names its sandbox is
  applied under that name, so applying it again updates the same sandbox.

- `exec` returns the command's output when it ends, each stream cut at one
  mebibyte, and takes `--cwd`, `--env` and `--timeout`. `run --ephemeral
  --rm` creates, waits, runs and deletes, including when the start fails
  or the command is interrupted. `logs <name>` reads the sandbox's main
  process output. A relative path given to a file command is resolved
  under `/workspace`.

### Removed

- `latere cella policy`, `rename`, `extend`, `convert`, `resize` and
  `wait`, background runs (`run <name> -- CMD`, `run --follow`,
  `run --detach`, `run status`, `run logs`, `run cancel`), and the
  `--credential`, `--idempotency-key` and `shell --session` flags. The
  Cella API has no counterpart for them, and each removed command exits 1
  and says what to use instead.

- `SANDBOX_API_URL` is no longer read; set `LATERE_CELLA_URL` instead.

## v0.12.0 - 2026-09-25

### Changed

- `latere models` replaces `latere lux`, and calls models through the
  Latere API at `https://api.latere.ai/v1/models` with your model key:
  `latere models` (or `models list`) lists the models your key reaches,
  `models env [--provider openai|anthropic|gemini]` prints the exports a
  stock SDK reads, `models invoke --model <name> "<prompt>"` makes one call,
  and `models key` and `models key revoke` show and revoke the key. Models
  are named as the catalog names them, such as
  `anthropic/claude-sonnet-4.6`. Prices are in the console's Models section,
  spend in its Billing section. `LATERE_MODELS_URL` or `--models-url`
  overrides the base URL, and a key handed in through `LATERE_MODEL_KEY`
  now needs no login.

- `latere review` runs its critics, and `latere topos --local` its model
  calls, through the Latere API with the model key. Both default to
  `anthropic/claude-sonnet-4.6` and take a model named as `latere models`
  lists it. `review --lux-url` is now `--models-url`.

### Removed

- `latere lux` and every command under it, with no alias:
  `lux access show/set/clear` (provider keys and bindings are over),
  `lux rates` and `lux providers` (a model's price is in the console),
  `lux usage` (spend is in the console's Billing section), `lux token` (use
  `latere models env --raw`), and `lux serve`, the tunnel that exposed a
  model running on this machine.

- `LUX_API_URL`, `--lux-url`, `LATERE_LUX_TOKEN`, and the `--token` flag of
  `latere review`: the model key is the only credential for a model call.
  A CI job hands the CLI a key with `LATERE_MODEL_KEY`.

## v0.11.1 - 2026-09-24

### Fixed

- v0.9.0, v0.10.0 and v0.11.0 were tagged but never published, so
  `install.sh` and `latere upgrade` kept installing v0.8.1. This release is
  published and carries their changes too: coming from v0.8.1, read their
  sections in
  [CHANGELOG.md](https://github.com/latere-ai/latere-cli/blob/main/CHANGELOG.md),
  and run `latere login` once, as v0.9.0 asks.

## v0.11.0 - 2026-09-24

### Added

- A model key for the Latere API. When `LUX_API_URL` points at
  `https://api.latere.ai/v1/models`, `lux env` and `lux invoke` present a
  key instead of your login: the CLI creates it at
  auth on first use, in your current context, allowed to use models and
  nothing else, and keeps it in the system keychain (or in
  `~/.config/latere/model-keys.json` with no keychain). `latere lux key`
  shows it and `latere lux key revoke` revokes it; `latere logout` revokes
  the keys the login created. `LATERE_MODEL_KEY` hands the CLI a key.

### Changed

- `latere login` requests only the standard OIDC scopes: `openid`, `email`,
  `profile` and `offline_access`. Topos access follows from the token's
  audience, so `latere topos` is unchanged and a token saved by an earlier
  release keeps working.

- The identity gate reads the frontend for the retired admin flag and
  refuses a second copy of the authorizer envelope (ci-gate v0.42.0).
  Nothing changes for a user of `latere`.

### Fixed

- v0.9.0 and v0.10.0 were tagged but never published, so `install.sh` and
  `latere upgrade` kept installing v0.8.1. v0.11.0 was tagged but not
  published either; v0.11.1 is the release that carries these changes.

## v0.10.0 - 2026-09-13

### Changed

- `latere whoami` prints the claims of the saved token and asks the issuer
  nothing; `latere auth login` with a pasted token confirms it at the
  issuer's `/api/me`. The issuer's token-introspection endpoint is gone
  from the family.

## v0.9.0 - 2026-09-13

- **Sign in again once.** Your login token now names `auth.latere.ai` and nothing else, so a token saved by an earlier release is refused at every product. Run `latere login`.
- Every product call presents a token minted for that one product and valid five minutes: `sandboxd` for `latere cella`, `toposd` for `latere topos`, `lux.latere.ai` for Lux, `drive.latere.ai` for `latere drive`, `origo` for git against Latere Code. Your login token reaches `auth.latere.ai` and no other service. A command that runs longer than five minutes mints again before its next request; a stream already in flight (`cella logs --follow`, a file export) keeps the token it opened with.
- `latere cella` no longer keeps a Cella-issued token. `~/.config/latere/token.json` and the `LATERE_TOKEN_FILE` variable are gone; `auth-token.json` is the one credential on disk, and `latere logout` clears it. Set `LATERE_CELLA_TOKEN` to present a bearer of your own to Cella, as `LATERE_DRIVE_TOKEN`, `LATERE_LUX_TOKEN` and `TOPOS_TOKEN` already do for their products.
- `latere print-token` prints the login token, which opens `auth.latere.ai` alone. To hand a credential to a product, ask for that product's own, e.g. `latere lux env --raw`.
- `lux env --ttl` is removed. Every token lives five minutes, the longest auth grants; re-run the command for a new one.
- `latere login`, `latere logout` and `latere whoami` no longer take `--api-url`. They speak to the auth service, which `--auth-url` or `AUTH_URL` selects.
- `latere topos`, `latere lux env`, `lux token`, `lux serve`, `latere review` and the local Lux model route no longer send your login token to Topos or Lux.
- `latere drive` and the git credential helper no longer fall back to a Cella-issued bearer when no login is saved. They refuse with `not logged in; run `latere login`` and send no request.
- `latere drive` now derives the auth URL from `DRIVE_API_URL` when no `--auth-url` or `AUTH_URL` is set, so a development Drive mints against the auth service beside it.
- `latere drive` commands now call Drive's API at `/v1` instead of `/api/v1`. They need the Drive release that serves `/v1`; against an older Drive they fail with 404.
- The git credential helper no longer answers for `drive.latere.ai`: Drive does not host git, and repositories live on Latere Code (`code.latere.ai`). `latere login` still wires the helper, for that host only. The `DRIVE_HOST` override is removed; `latere drive` and `DRIVE_API_URL` are unchanged.
- Cella `wait` and `logs` now detect status-output failures and honor configured stderr streams. Remote exit codes remain intact.
