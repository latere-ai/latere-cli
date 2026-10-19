# latere-cli Specs

Design records for the latere CLI (`github.com/latere-ai/latere-cli`). They
explain why each surface has the shape it has, what was tried and rejected, and
the constraints that are not obvious from the code. For how to *use* the CLI,
read the [README](../README.md) and [docs/](../docs/).

## Tree

```
specs/
  001-auth-unification-migration.md  (complete: adopts the shared device-code client and file token store)
  002-review-local-subcommand.md     (implemented: latere review; critics run through Lux and Topos)
  004-flatten-auth-commands.md       (implemented: latere login/logout/whoami/print-token/org as top-level verbs)
  005-lux-env-redesign.md            (implemented: lux env keyed by dialect and provider, with token provenance and TTL)
  006-model-key.md                   (implemented: a model key on first use, created at auth and kept in the keychain)
  007-models-at-the-origin.md        (implemented: latere models over the platform origin; latere lux retired)
  008-cella-at-the-origin.md         (implemented: latere cella on the Cella core under the platform origin)
  009-repos-command.md               (implemented: latere repos create, list and get on platformd's repository routes)
  010-app-command.md                 (implemented: latere app create, list, show, deploys, logs and delete on the Apps API)
  011-capability-commands.md         (implemented: latere environments and latere agents replace cella, topos and review)
  012-app-access.md                  (implemented: a new app is private; latere apps visibility, access, share and unshare)
  013-app-password.md                (implemented: latere apps password; access says a password, and visibility removes one)
  014-app-rename.md                  (implemented: latere apps rename; the app moves to a new slug and the latere remote follows)
  015-evals-commands.md              (in-progress: latere evals replaces latere eval; suites, experiments, comparisons, harness lease and import, publishing)
  016-devices-command.md             (complete: latere devices connects this computer as an environment the person's sessions work on; their own sessions start there unasked)
  017-signed-upgrades.md             (complete: releases sign their checksums; upgrade and auto-upgrade verify the signature, and an SSH signature beside it for the installer)
  018-a-desktop-client-of-the-device-engine.md (in-progress: serve connects, takes a client's tokens, changes the policy and removes the device through its socket, built; a client's denied paths and logout beside an application, to come)
```

## Status

Where each spec stands:

- `001-auth-unification-migration.md`: the shared device-code client and file
  token store are in use.
- `002-review-local-subcommand.md`: `latere review` ships.
- `.archive/003-drive-subcommand.md`: `latere drive`, removed on 2026-10-02
  with Drive's retirement.
- `004-flatten-auth-commands.md`: session verbs are top-level
  (`latere login/logout/whoami/print-token/org`).
- `005-lux-env-redesign.md`: `latere lux env` takes a `--compat` dialect or a
  provider passthrough, and reports the exported token's provenance. The
  per-command lifetime knob it designed is gone; leaf id-03 of
  `latere-ai/specs/infrastructure/identity` fixed every token at the
  issuer's maximum.
- `006-model-key.md`: `latere lux` creates a model key at auth on first use
  against the Latere API, keeps it in the system keychain or a file beside
  the login, and `latere lux key` shows and revokes it. Since 007 these
  commands are `latere models`.
- `007-models-at-the-origin.md`: `latere models` lists, exports and calls
  the models at `https://api.latere.ai/v1/models` with the model key, and
  `latere review` and `latere topos --local` call them the same way. The
  `latere lux` namespace, the hosted plane it reached, and `lux serve` are
  gone.
- `008-cella-at-the-origin.md`: `latere cella` drives sandboxes on the Cella
  core at `https://api.latere.ai/v1/environments` through its exported Go
  client, with a token minted for the audience `cella`. The commands of the
  retired hosted sandbox API that the core has no counterpart for are gone,
  and each says why.
- `009-repos-command.md`: `latere repos` creates, lists and looks up Git
  repositories on platformd's repository routes at
  `https://platform.latere.ai`, with a token minted for the audience
  `api.latere.ai`.
- `010-app-command.md`: `latere app` creates apps, adds their git remote,
  lists and shows them, lists their deploys and follows a build to its
  outcome on the Apps API at `https://api.latere.ai/v1/apps`, with a token
  minted for the audience `insula`. Deploying is a git push.
- `011-capability-commands.md`: the command groups are named after the
  platform's capabilities. `latere environments` replaces `latere cella`,
  and `latere agents run`, `provider` and `review` replace `latere topos
  --local`, `latere topos login` and `latere review`; the old words are
  refused with their replacement for one release.
- `012-app-access.md`: `latere apps create` names no visibility, so a new
  app is private unless `--visibility unlisted` is given.
  `latere apps visibility` changes it between `private` and `unlisted` on
  the Apps API, and `access`, `share` and `unshare` list, invite and remove
  the people invited to an app on platformd's access routes at
  `https://platform.latere.ai`, with the token `latere repos` mints. The CLI
  never sets `public`, which only a listing in the gallery gives.
- `013-app-password.md`: `latere apps password` sets, replaces and removes
  the password that opens a private app for anyone who types it, on
  platformd's password routes beside the access list. The password is typed
  at a prompt, read from standard input or generated, never a flag's value
  or an argument, and printed only when the command generated it. `access`
  names which of the three choices an app is on, and `visibility` removes a
  password with either of its values, changing the app whatever the platform
  answers.
- `014-app-rename.md`: `latere apps rename <new-slug> [slug]` moves an app
  to a new slug on the Apps API, which is its address and the name of its
  repository. The command checks the slug's form before it sends anything,
  says that the old address stopped working with no redirect and that the
  old slug is held for seven days, and sets the git remote `latere` when it
  held the app's old push URL, leaving any other remote with the line to
  run. Built on 2026-10-12, not released.
- `015-evals-commands.md`: `latere evals` on Evals at the platform origin,
  replacing the retired `latere eval` group: manifests applied from YAML,
  experiments run and followed, comparisons in the terminal, an external
  harness's lease, renewal and submit, a harness's past runs imported as
  declared trials, reports, benchmarks and datasets published and shared,
  and bar checks for CI with fixed exit codes.
- `016-devices-command.md`: `latere devices connect` connects this computer
  as one of the person's environments and serves it in the foreground as a
  Cella worker on the local driver, confined to the folders the person
  grants. The person's own sessions and scheduled triggers start there
  without asking and are shown as they start, a trigger on delivered
  events asks in the terminal, and `session_start: ask` makes every
  session ask (amended on 2026-10-19); `latere devices pause` stops them
  all. Every operation goes to an activity log on the computer, and the
  environment key is kept in the keychain. `latere devices serve` runs the
  same engine with no terminal, driven through a local control socket,
  which is what a desktop application builds on. `latere logout` removes a
  connected computer before it signs out. Released in v0.22.1 and checked
  live on 2026-10-19.
- `017-signed-upgrades.md`: each release signs `checksums.txt` with an
  Ed25519 key through `tools/sign`, and `latere upgrade` and auto-upgrade
  verify it against keys built into the binary before they read any
  checksum, refusing a release before v0.21.0. It comes before 016, since
  a connected computer upgrades itself. Released in v0.21.0. Releases after
  v0.22.1 also carry `checksums.txt.sshsig`, the same key's SSH signature,
  which the installer can verify with `ssh-keygen`; the installer's
  verification is the platform's to build.
- `018-a-desktop-client-of-the-device-engine.md`: what a desktop
  application needs of 016's engine to run it with no terminal: `serve`
  waits on a computer not yet connected and connects through the socket
  with the consent the application showed, takes short-lived Cella and
  platform tokens from the application in place of the saved login,
  changes the host policy and removes the device through the socket,
  makes no release check under `LATERE_NO_UPDATE_CHECK`, denies paths the
  application names, and `logout` keeps a device the application serves.
  Drafted on 2026-10-19 for Latere Studio's This Mac.

Two surfaces shipped without a dedicated design record: the token-lifecycle
work, which [docs/login-and-tokens.md](../docs/login-and-tokens.md) documents,
and the top-level `eval` command group, which has code but no spec or guide.
That is a recorded decision, not an open action item.

## Conventions

- The CLI talks to the auth service (login, org switch) with the saved login
  token, and reaches every product backend (Cella, Origo, the platform,
  Apps)
  with a token minted at auth for that one product. The model endpoints
  take the model key instead (006, 007). It does not host an HTTP server,
  does not own a cookie session, and has no frontend.
- Token storage is `~/.config/latere/auth-token.json`: the login token, and
  nothing else. A product token lives in memory for one command. The model
  key is kept in the system keychain, or a 0600 file beside the login.
