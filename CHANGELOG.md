# Changelog

Every tag has a section here, and the section is the body of the GitHub
release. A tag without one is refused at the pre-push and fails the release
workflow. Write under `Unreleased` as work lands; `lateregate release vX.Y.Z`
turns that into the tag's section, commits, tags and pushes.

A section says what changed for whoever uses the release, not what was
committed: the commit log already holds that.

## Unreleased

### Added

- **`latere environments port-forward <name|id> <local>:<port>`** carries
  a port on your machine to a port inside a workload, the way into a
  workload's port now that the console does not open one. It listens on
  `127.0.0.1`, prints the address, carries every connection over a socket
  of its own and runs until interrupted. A port the workload's manifest
  does not declare in `spec.network.ports` is refused before it listens,
  since only a declared port is reached.

### Changed

- **On macOS, sessions on this computer cannot read Latere Studio's data or
  cache,** `~/Library/Application Support/ai.latere.studio` and
  `~/Library/Caches/ai.latere.studio`, where the app keeps its local copy
  of your conversations. The app denied them already when it serves the
  computer; now `latere devices connect` and `serve` deny them too.

### Removed

- **The old command words and variables are gone.** They were refused with
  their replacement since v0.17.0, and are now unknown commands and unread
  variables. Use the capability names:
  - `latere cella` and `latere sandbox` are `latere environments`.
  - `latere topos --local` is `latere agents run`, and `latere topos login`
    is `latere agents provider`.
  - `latere review` is `latere agents review`.
  - `latere app` is `latere apps`.
  - `LATERE_CELLA_URL` and `LATERE_CELLA_TOKEN` are
    `LATERE_ENVIRONMENTS_URL` and `LATERE_ENVIRONMENTS_TOKEN`.
  - `LATERE_TOPOS_PROVIDER_FILE` is `LATERE_AGENT_PROVIDER_FILE`.
  - `LATERE_APP_URL` and `LATERE_APP_TOKEN` are `LATERE_APPS_URL` and
    `LATERE_APPS_TOKEN`.

  An old variable left in your shell is now ignored rather than refused,
  so a script that still sets one reaches the default address instead of
  the one it named.

## v0.31.0 - 2026-10-21

### Changed

- **`latere devices` reaches the platform through the Latere Go SDK
  v0.3.0.** Its commands, flags, words and exit codes are as they were.
  Listing, renaming and removing a device, and the labels a connect reads
  for your account, are sent again after a temporary error (429 or 5xx) or
  a dropped connection, and so is the labels read of `latere environments
  apply` and `latere environments run --ephemeral`. `latere devices list
  --json` prints the platform's answer as it is sent: every field, such as
  `isolation`, and no `os`, `arch`, `last_seen` or `network` for a device
  that has none, where before they were empty or null.

## v0.30.0 - 2026-10-21

### Changed

- **`latere agents review` reports what its critics spend.** Each critic
  round is one call to the critic model through the Latere API, with your
  model key as before, and its tokens now count toward `--cost-cap`
  beside the proposer's. The flags, the output and the exit codes are as
  they were. `--models-url` and `LATERE_MODELS_URL` must end in
  `/v1/models`, as the default does; another address is refused before a
  round runs.
- **`latere agents run` runs on Topos's current runtime.** The interactive
  session, `-p`, `--dir`, `--model`, `/model`, `/help` and `/quit` work
  as before, and so does the model order: `ANTHROPIC_API_KEY`, then the
  provider `latere agents provider` saved, then the Latere API with your
  model key. Ollama works as before, through its OpenAI endpoint; the
  agent reads the model's context length from Ollama at the session's
  first turn, and refuses a model Ollama reports none for. The agent still
  runs every call without asking. Its file tools now reach the working
  directory alone and refuse your credential files, and a model it has no
  context window or output limit for is refused when the turn starts.
- **`latere devices` runs Cella's shared device engine.** Connecting,
  serving, approving sessions, the activity log and the control socket
  are the engine Latere Studio runs too, so the terminal and the
  application serve one device the same way. Its commands, flags, words
  and exit codes are as they were, and so are the files it keeps: the
  host policy file, the key, the activity log and the owner record. A
  computer already connected keeps working with no step.

### Removed

- **Claude sign-in is no longer a provider for `latere agents run`; use an
  Anthropic API key or the Latere API.** `latere agents provider` no
  longer offers the browser sign-in, `CLAUDE_CODE_OAUTH_TOKEN` is no
  longer read, and a Claude sign-in saved by an earlier version stops the
  agent with a message that names `latere agents provider`. The
  `claude.json` an earlier version saved is no longer read.

### Security

- Built with Go 1.27.3, which fixes GO-2026-6809.

## v0.29.1 - 2026-10-20

### Changed

- **The `latere apps` commands use the Go SDK's first release, v0.1.0.**
  A rename or visibility change the platform answers with a server error
  is tried again, as a read already was. A refusal that is not the
  platform's JSON, such as a proxy's page, still shows its own text, now
  cut at a whole character rather than in the middle of one.

## v0.29.0 - 2026-10-20

### Changed

- **`latere apps` reaches the platform through the Latere Go SDK.** Its
  commands, flags, output and exit codes are as they were. `--api-url` and
  `LATERE_APPS_URL` must end in `/v1/apps`, as the help has always said;
  another address is refused before anything is sent. `latere apps list`
  ends at the page that names no next one.
- **`latere apps` rides out a brief outage.** A read or a removal the
  platform answers with a temporary error (429 or 5xx), or that a dropped
  connection cuts off, is sent again after a short wait, and a request
  answered 401 is sent once more with a fresh token. A followed build log
  that drops picks up after the last line it showed. A refusal that lasts
  is reported as before.

## v0.28.0 - 2026-10-20

### Changed

- **A model command makes your personal key whatever your wallet holds.**
  In your personal account, `latere models` and the other model commands
  no longer stop with "no model key was made" when your wallet has never
  held credit you bought. They make the key, and its calls spend your
  wallet, the monthly credit Latere gives you included.

## v0.27.1 - 2026-10-20

### Fixed

- **`latere` builds for Windows again.** v0.27.0 was tagged but not
  published: the sandbox's launcher, which `latere devices` needs on
  macOS and Linux, was reached on Windows too, where it does not build.
  This release carries v0.27.0's changes.

## v0.27.0 - 2026-10-20

### Changed

- **On macOS, `latere devices` needs nothing installed.** Every command a
  session runs is confined by Seatbelt, part of macOS, in place of srt,
  Node.js and ripgrep. Linux keeps srt.
- **On macOS a host on a device's allowlist is reached on ports 443 and
  80;** write `host:port` for another, as `github.com:22` for git over SSH.
- **On macOS a session cannot write, inside a folder you shared, the files
  your own tools later run as code**: shell startup files, `.envrc`,
  `.gitconfig`, `.gitmodules`, `.mcp.json`, `*.code-workspace`, the
  `.claude`, `.cursor`, `.vscode` and `.idea` folders, and a repository's
  config and hooks. Everyday git works there; `git init`, `clone`,
  `config` and `remote add` do not.

## v0.26.0 - 2026-10-19

### Changed

- **Renaming a device from a desktop application leaves its running
  sessions alone.** Only a change of the folders or the network restarts
  the connection, which stops the commands running; a new name does not.
- **A desktop application's tokens must be yours.** `serve --credentials
  client` refuses a token in an organization's context
  (`credentials_org_context`). Once the platform has confirmed whose device
  this is, it refuses another person's token (`credentials_other_person`).
  The owner is recorded in `owner.json` in the state directory when you
  connect, from the terminal or through the socket, and, for a device
  connected before this release, the first time the platform mints the
  computer's key for a person or answers their sign-in as the device's
  own. Until then, tokens the platform answers as not the device's
  owner's are dropped and the device is kept, so an application signed in
  as someone else never makes the computer forget your device.
- **`serve --credentials client` waits for the application's sign-in to
  mint this computer's key** instead of stopping, offline meanwhile, with
  its control socket up.
- **The consent covers everything a connect grants.** The facts a connect
  states, in the terminal and to an application, now name the paths read
  outside the folders and the remembered scheduled triggers still within
  their date that the host policy file kept, when it keeps any; a file
  changed after the proposal fails the consent.

### Added

- `GET /v1/status?follow=1` streams the status each time it changes, so an
  application learns at once that a token is needed.
- `POST /v1/connect` answers `warnings` when the computer is connected and
  its name was refused.

### Fixed

- A rename from an application no longer holds up sessions starting while
  the platform answers.
- Stopping `serve --credentials client` while a key renewal waits for the
  application's sign-in ends it cleanly, not with
  `device_sign_in_expired`.
- A step that needs the application's token and found none is refused
  `device_credentials_needed`, not as a refused connect, rename or
  removal.
- A proposal with no folder asks for one, instead of proposing the folder
  `serve` runs in.
- Removing a device while the computer is paused deletes its sandboxes; a
  removal right after a connect never leaves the device served; a removal
  deletes the host policy file even when the connection saw the removal
  first; and a key replaced before a removal is no longer revoked against
  the next device.

## v0.25.0 - 2026-10-19

### Added

- **A desktop application can run `latere devices serve` without a
  terminal from start to finish.** On a computer that is not connected,
  `serve` now waits with its control socket open instead of refusing,
  and the application connects it there: `GET /v1/connect` answers the
  proposal with the facts the connect screen states and a consent
  digest, and `POST /v1/connect` with that consent connects the computer
  as typing yes does. After the device is removed, `serve` waits again.
- **`serve --credentials client` never reads your saved sign-in.** The
  application hands it short-lived Cella and platform tokens through
  `PUT /v1/credentials`, kept in memory only, and `GET /v1/status` says
  when one is needed again.
- **The host policy and the device through the socket:** `GET` and `PUT
  /v1/policy` read and change the name, folders and network, checked as
  `connect` checks them; `DELETE /v1/policy/remembered/{trigger}` forgets
  a remembered trigger; `POST /v1/remove` removes the device as `latere
  devices remove` does, or with `{"local": true}` forgets it here alone.

### Changed

- `latere devices serve` makes no daily release check under
  `LATERE_NO_UPDATE_CHECK` or `CI`, as every other release check already
  does, so a copy an application bundles never looks for a release.

## v0.24.3 - 2026-10-19

### Fixed

- The notice that a new release is available no longer names a release
  `latere` refused because it is not signed by Latere. It used to follow
  the refusal with "Run `latere upgrade` to update." and repeat it every
  day, though `latere upgrade` refused that release too. `latere upgrade`
  now remembers its own refusal as auto-upgrade does, and `latere upgrade
  --check` says when the latest release was refused. A newer release is
  announced as before.

## v0.24.2 - 2026-10-19

### Changed

- A connected computer reads as online once its connection to Latere is
  open, rather than as soon as it registers, and says why when it is
  offline. A file a session asked for that is not there reads as
  "not found: <path>" in the activity log, rather than "environment not
  found".

### Fixed

- On the control socket of `latere devices`, the answer to a session's
  request now always comes before the `started` event of the session it
  lets in, so a desktop application reads them in order.

## v0.24.1 - 2026-10-19

### Changed

- Auto-upgrade checks for a new release once a day again, rather than
  before every command. When the daily check finds one, it is still
  installed before that command runs, not on a later run. A release found
  but not installed, as after a failed download, is tried again on the
  next command. Run `latere upgrade` to install a release sooner.

## v0.24.0 - 2026-10-19

### Changed

- Auto-upgrade now checks for a new release before each command you run
  in a terminal and installs it before running the command. It used to
  check at most once a day and install on a later run, so a release could
  wait a day or more. The check takes a fraction of a second; when it
  fails, as it does offline, the command runs as it is and the next check
  waits ten minutes. Scripts and pipes are not checked. With
  `latere upgrade --auto off`, or a `latere` in a directory you cannot
  write, the check stays once a day and prints a notice.

## v0.23.0 - 2026-10-19

### Changed

- On a connected computer, the sessions you start, and your triggers that
  run on a schedule, now start without asking on the computer. Each prints
  one line as it starts, is written to the activity log, and is told to a
  desktop application on the control socket. A trigger that runs on mail
  or a webhook still asks there first. Each command that changes something
  is still approved in the chat, unless you approved a matching one before.
  This is the new `session_start: allow` in the host policy file, and a
  computer connected before this release has it too: `connect` says so at
  every start. To be asked before every session as before, run
  `latere devices policy --session-start ask`, which applies to the next
  session even while the computer is serving. Once this release writes
  `session_start` into the file, an older `latere` refuses the file; remove
  that line before going back to one.

### Added

- `latere devices pause` stops every command a session started on this
  computer at once and keeps sessions out, while `connect` keeps running;
  `latere devices resume` lets them back in.
- `latere devices connect --session-start allow|ask` and `latere devices
  policy --session-start allow|ask` set how sessions start.
- Each release also carries `checksums.txt.sshsig`, Latere's signature of
  `checksums.txt` in the format `ssh-keygen` checks, made with the same key
  as `checksums.txt.sig`, so a download can be verified with the
  `ssh-keygen` that macOS and Linux already have; the README shows the
  command. `latere upgrade` checks `checksums.txt.sig` as before.

## v0.22.1 - 2026-10-19

### Changed

- No change for people using `latere`. v0.22.0's release did not publish, so
  this release carries its changes, listed under v0.22.0 below.

## v0.22.0 - 2026-10-19

### Added

- `latere devices connect` connects your own computer as one of your
  environments, so the sessions you start can run commands and read and
  edit files in the folders you share, with your own tools. The first time,
  it shows the folders, the network and what the agent reads, and connects
  only after you type `yes`; it then serves the computer in the foreground
  until Ctrl-C, which stops every command a session started there. Each
  session asks for your approval on the computer before it starts; a
  trigger that fires on a schedule can be remembered for 30 days, and one
  that fires on delivered events always asks. Commands run in the sandbox
  of `srt`, confined to the folders you share, with your credentials,
  keychains, browser profiles and `latere`'s own files denied even inside
  them. macOS and Linux, from your personal context, never as root, and
  only on a build that verifies its own upgrades.
- The host policy file `~/.config/latere/device.yaml` holds what sessions
  may use: the folders, read-write or read-only, the network (an allowlist
  of hosts, or none), read paths, and remembered triggers. `latere devices
  policy` prints and checks it and forgets a remembered trigger.
- Every operation a session asks of the computer, and every approval, is
  written to an activity log with no file content or output, rotated at
  10 MB. `latere devices activity` prints it and follows it with `-f`.
- `latere devices serve` serves the computer with no terminal, for a
  desktop application or a background service, which answer approvals and
  read the status and the activity through a local socket only your own
  user can open.
- `latere devices list`, `rename` and `remove` manage your devices.
- The computer's key is kept in the keychain, or in
  `~/.config/latere/device-keys.json` where there is none, lasts 30 days,
  and is replaced weekly. `LATERE_DEVICE_KEYS_FILE` keeps it in a file of
  your choice instead.

### Changed

- `latere logout` on a connected computer says that signing out removes it
  from your devices, asks, and removes it before it signs out. If the
  device cannot be removed, you stay signed in. `--yes` answers for you.

## v0.21.0 - 2026-10-18

### Changed

- Releases are signed. Every release from this one carries
  `checksums.txt.sig`, Latere's Ed25519 signature of `checksums.txt`, and
  `latere upgrade` and auto-upgrade check it against the keys built into
  `latere` before they trust any checksum. A release whose signature is
  missing or does not verify is refused with "this release is not signed by
  Latere; nothing was changed", and the installed binary stays as it was.
  There is no option to skip the check. When auto-upgrade refuses a
  release, it says so once and tries that release again a day later.
- `latere upgrade vX.Y.Z` refuses a release before v0.21.0, the first
  signed release, so a rollback stops there. To run an older release,
  download it from the releases page.

## v0.20.1 - 2026-10-17

### Fixed

- `latere environments apply` and `latere environments run --ephemeral`
  add your account's labels to every workload they create. The platform
  refuses a create without them, so `run --ephemeral` could create nothing
  and `apply` worked only with labels copied into the manifest by hand.
  The CLI reads the labels for your current context from the platform at
  `/v1/account/environments/context` and sets them in the manifest before
  it sends it, keeping the manifest's own labels, comments and syntax; a
  value the manifest gave one of the three is replaced. When the platform
  cannot answer them, the command stops before it creates anything.
  `LATERE_PLATFORM_URL` and `LATERE_PLATFORM_TOKEN` select the platform
  and its bearer, as for `latere repos`. Needs the platform release that
  serves the labels read.
- The help of `latere environments`, `apply` and `run --image` names all
  three images of the platform's catalog, `base`, `gui` and `science`,
  where it named only `base` and `gui`.
- `latere models env` no longer offers `--provider gemini`. The Gemini door
  reaches no model of the catalog, which is served in the OpenAI dialect, so
  its exports pointed an SDK at nothing it could call. Naming it now exits 1
  and says to use `--provider openai` or `--provider anthropic`, which reach
  every catalog model, Google's included.
- A model command makes no personal model key before your wallet has held
  credit you bought, as the console's Keys section makes none. It asks the
  platform's wallet read first, and when the wallet holds only credit Latere
  gave, it says so, makes nothing at auth, and points at the console's
  Billing section; before, it made a key that showed Waiting for credit and
  reached no model. A key nearing its end is replaced only when a new one
  may be made; until then the CLI keeps using it, notes when it ends, and
  says why it is not replaced. In an organization's context nothing changes.

## v0.20.0 - 2026-10-12

### Added

- `latere apps rename <new-slug> [slug]` changes an app's address. The app
  and its previews answer at `https://<new-slug>.latere.site` at once, and
  the command prints the new address and the new push URL. The old address
  stops working at once and is not redirected, so links to it stop working.
  The old slug stays held for your account for seven days: nobody else can
  take it in that time, and renaming the app back takes it again. In a git
  repository whose remote `latere` is the app's old push URL, the command
  sets that remote to the new one; a remote that points anywhere else is
  left as it is, and the command prints the `git remote set-url` line to
  run. A slug that breaks a rule is refused before anything is sent, each
  refusal of the platform says its code and its reason and exits 1, and
  `--json` prints the app as the API answers it. Without `[slug]` the app is
  the one the `latere` remote names, as for the other `apps` commands.

## v0.19.0 - 2026-10-12

### Changed

- `latere apps visibility` removes the app's password when it has one, with
  either value, and says so. `private` means only people invited, so the
  people who used the password no longer get in. An `unlisted` app opens for
  anyone with the link, where a password decides nothing, and one left set
  would open the app again the next time it is private. The password is kept
  by the platform, which the command now asks as well, at `--platform-url`.
  The visibility is changed whatever the platform answers: when it cannot be
  asked, or does not remove the password, the command says so in one line on
  standard error, with the command that removes a password, and exits 0.
- `latere apps visibility` and `latere apps access` name who can open an app
  by the same three choices the console and the chat offer: `Only people
  invited`, `Anyone with the password` and `Anyone with the link`. The
  sentence each prints starts with the choice, so a script that matched the
  sentence of v0.18.0 needs the new one. `visibility` prints the sentence of
  v0.18.0 when the platform cannot be asked, and `--json` is unchanged.

### Added

- `latere apps password [slug]` sets a password on an app, or replaces the
  one it has. Anyone who types the password at the app's address opens the
  app and its previews, with no Latere account, while you, the members of
  the organization that owns the app and the people you invited still sign
  in. The password is typed at a prompt that asks twice and shows nothing,
  read from one line of standard input with `--stdin`, or made by
  `--generate`, which prints it once on the last line of the output. It is
  never a flag's value or an argument. Setting a password makes the app
  private when it is not, and the command says so. `--json` carries a
  password only when `--generate` made it. Where the platform does not offer
  passwords yet, the command says so and changes nothing.
- `latere apps password --remove [slug]` removes the password: the people
  who used it no longer get in. It is no error when the app has none.
- `latere apps access` says whether an app has a password and since when,
  and `--json` carries the platform's `password`. A password left on an app
  anyone with the link opens is said with the command that removes it.

## v0.18.0 - 2026-10-11

### Changed

- **A new app is private.** `latere apps create` no longer makes every app
  public. It names no visibility, so the platform decides, and a new app
  starts private: only you, or the members of the organization that owns it,
  and the people you invite can open it, each after signing in with a Latere
  account. The command prints the visibility the app was created with, who
  can open it, and for a private app the two commands that open it or share
  it. `latere apps create --visibility unlisted` creates an app open to
  anyone who has its address, which is what a script that relied on a
  created app being open needs now. The apps you already have keep their
  visibility.
- `latere apps list` has a `VISIBILITY` column, and `latere apps show` says
  who the app's visibility lets open it.
- A refusal from the platform prints the detail it carries, for
  `latere repos` as well as for the commands below.

### Added

- `latere apps visibility <private|unlisted> [slug]` changes who can open an
  app at its address and its previews. `unlisted` opens it to anyone who has
  the address, without signing in, and keeps it out of search engines.
  `private` closes it to everyone but you and the people you invite. The
  command line does not take `public`: an app becomes public when it is
  shown in the gallery, which you ask for from the app's Share in the chat
  and which is reviewed first. An app that is public already reads as public
  in every command, and making it unlisted or private takes it out of the
  gallery.
- `latere apps share [slug] <email>...` invites people to a private app by
  email. Invitations are not emailed: the command prints the app's address
  for you to send, and each person opens it after signing in with a Latere
  account whose verified email is the address you invited. Your own address
  and an address already invited are skipped, and the command says so. On an
  unlisted or public app it says the app is already open to anyone who has
  its address, and that the invitations decide who opens it once it is
  private.
- `latere apps unshare [slug] <email>...` takes invitations back by address.
  An address that is not invited is reported and does not fail the others.
- `latere apps access [slug]` shows who can open an app and lists the people
  invited: the address, whether the invitation is pending or active, the
  person's name once they accepted it, and how long ago they were invited.
  `--json` prints the app and its access list as the APIs answer them.
- `share`, `unshare` and `access` take the slug from the git remote `latere`
  when it is left out, like the other `latere apps` commands. They keep the
  people invited at the platform, `https://platform.latere.ai`, with a token
  minted for it alone: `--platform-url`, `LATERE_PLATFORM_URL` and
  `LATERE_PLATFORM_TOKEN` apply as they do to `latere repos`.

## v0.17.1 - 2026-10-11

### Changed

- **Install with the platform's address alone: `curl -fsSL https://platform.latere.ai | sh`.** platform.latere.ai answers curl and wget with the installer at its front page. `latere upgrade` gives this command when it cannot replace a binary it may not write, and the README uses it. `https://latere.ai/install.sh`, the address before it, is removed. Pinning a version and a system-wide install are unchanged: `| sh -s -- vX.Y.Z` and `| PREFIX=/usr/local sh`.
- **The installer lives with the platform that serves it.** `install.sh` is no longer in this repository, and what a person runs is the script of the platform's last release, not this repository's main branch.

### Security

- Built with Go 1.27.2 and golang.org/x/net v0.60.0, which fix GO-2026-6611, GO-2026-6612, GO-2026-6613 and GO-2026-6617.

## v0.17.0 - 2026-10-03

### Changed

- The commands are named after the platform's capabilities, as `latere models`
  and `latere repos` already were:

  | Before | Now |
  |---|---|
  | `latere cella <command>`, alias `latere sandbox` | `latere environments <command>`, every command kept |
  | `latere topos --local` | `latere agents run` (no `--local`: it always runs on this machine) |
  | `latere topos login` | `latere agents provider` |
  | `latere review` | `latere agents review` |
  | `LATERE_CELLA_URL`, `LATERE_CELLA_TOKEN` | `LATERE_ENVIRONMENTS_URL`, `LATERE_ENVIRONMENTS_TOKEN` |
  | `LATERE_TOPOS_PROVIDER_FILE`, `topos-provider.json` | `LATERE_AGENT_PROVIDER_FILE`, `agent-provider.json` |
  | `latere app <command>` | `latere apps <command>`, every command kept |
  | `LATERE_APP_URL`, `LATERE_APP_TOKEN` | `LATERE_APPS_URL`, `LATERE_APPS_TOKEN` |

  For this release the old words exit 1 and name their replacement, whatever
  followed them, and the release after removes them. An old variable set
  without its replacement is refused with the new name rather than ignored;
  with both set, the new one is used. A provider choice saved in
  `topos-provider.json` is moved to `agent-provider.json` the first time it is
  read. Help and output say workload for what Environments runs: the list
  labels a record `workload:` where it said `cella:`, and `--json` output is
  unchanged. `latere eval` keeps its name for now.

## v0.16.0 - 2026-10-03

### Added

- `latere app` creates apps on the platform and follows their builds.
  Deploying is a git push to the app's repository, which `latere login`
  already lets git sign in for.
  - `latere app create [name] [--slug s]` creates a public app in your
    current context, prints its address, its newest-preview address and its
    push URL, and in a git repository adds the push URL as the remote
    `latere` (`--remote <name>` names it, `--no-remote` skips it). A
    repository whose `latere` remote names another app is refused before
    anything is created. A refused create says why: a taken, held (with the
    hold's end) or reserved slug, or the rule an invalid slug breaks.
  - `latere app list`, `show [slug]` and `deploys [slug]` show your apps,
    what production serves, the newest preview, the push URL, and the
    deploys with their ref, status and preview address. Each takes `--json`.
  - `latere app logs [slug] [deploy] [-f]` prints a build log, the newest
    deploy's unless a deploy id or 8-character prefix is given. `-f` follows
    it and exits 0 when the build succeeds and 1, with the failure's code and
    message, when it fails or is canceled. Inside the app's repository it
    follows the deploy of the commit `HEAD` names and waits up to a minute
    for it to appear, so `git push latere main && latere app logs -f` is one
    step. The deploy of a release builds nothing, so for it the command says
    which preview it released and prints that preview's build log. Color
    codes are kept on a terminal and removed otherwise.
  - `latere app delete <slug>` asks for the slug typed back unless `--yes`;
    the slug stays held for your account for seven days.
  - A `[slug]` left out is read from the git remote `latere`. The commands
    call `https://api.latere.ai/v1/apps` with a token minted for the
    audience `insula`; `LATERE_APP_URL` or `--api-url` overrides the address,
    and `LATERE_APP_TOKEN` presents a bearer as given.

## v0.15.0 - 2026-10-02

### Removed

- `latere drive` and its flags, `DRIVE_API_URL` and `LATERE_DRIVE_TOKEN`.
  Drive was retired on 2026-09-19 and its address no longer answers, so
  every one of these commands failed to connect. Files on Latere are in
  Storage, at `api.latere.ai/v1/storage` and in the console.

### Changed

- `latere print-token` refreshes a saved login that is due before printing
  it, as `latere whoami` does, so a script no longer receives a token that
  expired since the last command. It takes `--auth-url` like `whoami`.
- `latere.ai/x/pkg` v0.90.2 and OpenTelemetry Go v1.46.0, past GO-2026-6615 and
  GO-2026-6505.

## v0.14.0 - 2026-09-29

### Added

- `latere repos create <owner>/<name> [--public]` creates a Git repository on
  Latere Code under your handle, or under the organization you are in as an
  owner or admin, private unless `--public` is given, and prints its id and
  clone URLs. `latere repos list` shows your current context's repositories
  and, in your personal context, the ones shared with you, with your role on
  each; `latere repos get <owner>/<name>` shows one. Each takes `--json`. The
  commands call the platform at `https://platform.latere.ai` with a token
  minted for the audience `api.latere.ai`; `LATERE_PLATFORM_URL` or
  `--platform-url` overrides the address, and `LATERE_PLATFORM_TOKEN`
  presents a bearer as given. A refusal prints the platform's code and
  sentence and exits 1.

## v0.13.1 - 2026-09-28

### Removed

- `latere topos` no longer reaches the hosted Topos service at
  `topos.latere.ai`, which is retired. The home screen,
  `latere topos agents`, `latere topos session` and
  `latere topos serve-sandbox` are gone, with `--api-url`,
  `TOPOS_API_URL` and `TOPOS_TOKEN`, and `latere topos` without `--local`
  exits 1 and says so. The hosted sessions only ever ran a built-in
  stand-in model, and they are not migrated. `latere topos --local` and
  `latere topos login` are unchanged.

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
