# Changelog

Every tag has a section here, and the section is the body of the GitHub
release. A tag without one is refused at the pre-push and fails the release
workflow. Write under `Unreleased` as work lands; `lateregate release vX.Y.Z`
turns that into the tag's section, commits, tags and pushes.

A section says what changed for whoever uses the release, not what was
committed: the commit log already holds that.

## Unreleased

- `latere topos`, `latere lux env`, `lux token`, `lux serve`, `latere review` and the local Lux model route no longer send your login token to Topos or Lux. Each presents an actor token minted for that one product, audience `toposd` or `lux.latere.ai`, valid five minutes; a session that runs longer re-mints a minute before expiry. `lux env` without `--ttl` now exports a five-minute token and says so; re-run the command for a new one.
- `latere drive` and the git credential helper no longer fall back to the Cella bearer in `token.json` when `auth-token.json` is absent, as it is after a `latere login --token` paste login. They now refuse with `not signed in; run `latere login`` and send no request. The Cella bearer named Cella alone, so Drive and Latere Code rejected it anyway.
- `latere drive` commands now call Drive's API at `/v1` instead of `/api/v1`. They need the Drive release that serves `/v1`; against an older Drive they fail with 404.
- The git credential helper no longer answers for `drive.latere.ai`: Drive does not host git, and repositories live on Latere Code (`code.latere.ai`). `latere login` still wires the helper, for that host only. The `DRIVE_HOST` override is removed; `latere drive` and `DRIVE_API_URL` are unchanged.
- Cella `wait` and `logs` now detect status-output failures and honor configured stderr streams. Remote exit codes remain intact.
