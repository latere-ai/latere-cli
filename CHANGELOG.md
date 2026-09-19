# Changelog

Every tag has a section here, and the section is the body of the GitHub
release. A tag without one is refused at the pre-push and fails the release
workflow. Write under `Unreleased` as work lands; `lateregate release vX.Y.Z`
turns that into the tag's section, commits, tags and pushes.

A section says what changed for whoever uses the release, not what was
committed: the commit log already holds that.

## Unreleased

### Removed

- **The storage commands are gone.** `latere drive` and everything under it
  (`ls`, `get`, `put`, `mv`, `rm`, `restore`, `history`, `share`, `shares`,
  `unshare`) no longer exist, and neither does `DRIVE_API_URL` or
  `LATERE_DRIVE_TOKEN`. Latere Drive is retired, and storage from the
  terminal returns with the redesigned CLI that speaks to the platform's
  API origin. Until then, use the Storage section of the console. Nothing
  else in `latere` changes: sign-in, `cella`, `lux`, `topos`, `review`,
  `eval` and the git credential helper are untouched.

### Changed

- The identity gate reads the frontend for the retired admin flag and
  refuses a second copy of the authorizer envelope (ci-gate v0.42.0).
  Nothing changes for a user of `latere`.

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
