---
title: "`latere devices`: connect this computer as one of the person's environments and serve it in the foreground through Cella's embedded worker and local driver, the host policy file of granted folders, network and remembered scheduled triggers, the person approving each session in the terminal or through the engine's control socket, `latere devices serve` for a desktop application or a background service, an activity log of every operation, the environment key in the keychain and rotated, and list, rename, remove, policy and activity"
status: complete
depends_on:
  - specs/001-auth-unification-migration.md
  - specs/008-cella-at-the-origin.md
  - specs/011-capability-commands.md
  - specs/017-signed-upgrades.md
  - "CROSS-REPO: latere-ai/specs decisions/2026-10-18-a-persons-computer-is-an-environment-only-they-use.md, the four points; infrastructure/devices.md, the family's view"
  - "CROSS-REPO: latere-ai/cella specs/081-local-driver.md, the driver this command runs"
  - "CROSS-REPO: latere-ai/cella specs/082-the-worker-as-a-library.md: `worker.Run`, `HostPolicy`, `Record`"
  - "CROSS-REPO: latere-ai/cella specs/083-environments-a-subject-owns.md: an environment the person owns, key `ttl`, `serve_refused`, `key_revoked`"
  - "CROSS-REPO: latere-ai/platform specs/220-devices.md: what platformd allows at create, `/v1/account/devices`, removal, `device_limit`"
affects:
  - internal/commands/devices.go, devices_connect.go, devices_records.go, devices_other.go (new; `pause`, `resume` and `--session-start` from 2026-10-19)
  - internal/devices/ (new: the engine, the host policy file, the key store, the activity log, the approvals, the terminal client, the control socket, the lock)
  - internal/commands/auth.go (`logout` warns, asks and removes the device on a connected computer)
  - internal/commands/root.go (the `devices` group)
  - cmd/latere/devices_e2e_test.go (new: the end-to-end run against a real cellad)
  - go.mod (`latere.ai/x/cella` v0.17.2, with `worker` and `runtime/local`; v0.18.0 from 2026-10-19, for `worker.Options.State`)
  - docs/devices.md (new), docs/configuration.md, docs/login-and-tokens.md, README.md, CHANGELOG.md
effort: large
created: 2026-10-18
updated: 2026-10-21
author: changkun
---

# `latere devices`

## Why

The maintainer decided on 2026-10-18 that a person's own computer
connects to the platform as one of their environments, so a hosted
session they start can work on it. The CLI is what connects it: it runs
Cella's worker in its own process, with Cella's local driver confining
every command to the folders the person granted.

This is the third attempt at the job. `latere topos serve-sandbox` and
`latere lux serve` each ran a tunnel of their own beside the cores, and
both were removed in September 2026. This command adds no tunnel: it is
a Cella worker, the same stream every self-hosted worker uses.

Two of the four points are this command's to keep: the computer approves
each session itself, and it is confined to the folders its owner grants.

## Design

### The commands

| Command | What it does |
|---|---|
| `latere devices connect [--name N] [--folder PATH[:ro]]... [--network allowlist\|none] [--host H]...` | The first time, connects this computer (below), then serves it. Later, serves the device this computer already is. Runs in the foreground until Ctrl-C |
| `latere devices list` | The person's devices from `GET /v1/account/devices`: name, OS, online, last seen, folders, with this computer marked |
| `latere devices rename NAME [ID]` | Renames this device, or another one of the person's, with `PATCH /v1/account/devices/{id}` |
| `latere devices remove [ID]` | Removes this device or another one with `DELETE /v1/account/devices/{id}`. On this computer it also deletes the key and the host policy file |
| `latere devices serve` | Serves a device this computer already is, with no terminal: the engine alone, driven through its control socket (below). This is what a desktop application or a background service runs |
| `latere devices policy` | Prints the host policy file's path and its content, and checks it |
| `latere devices activity [-f] [--since D]` | Prints the activity log, and follows it with `-f` |

### Connecting

1. **Refuse what cannot be safe.** The command refuses to run as root,
   and refuses on a build that cannot verify its own upgrades (spec 017).
   It takes a lock file in its state directory, so a second `connect` on
   the same computer stops with "this computer is already being served"
   before it reaches the platform.
2. **Personal context.** A device is personal. When the login is in an
   organization's context, the command stops and prints `latere org
   --personal`.
3. **The saved login.** The person's saved login, in their personal
   context, is what connects the computer; there is no fresh sign-in. With
   no saved login the command stops and prints `latere login`.
4. **The connect screen.** Before anything is created, it shows what the
   person is about to allow, and asks for a typed `yes`:
   - the folders sessions may use, read-only or read-write, and that
     everything in them is readable to the agent, a project's `.env`
     included;
   - the network, with the hosts listed, and that a host which accepts
     writes can carry data out;
   - that only sessions the person starts, or triggers they own, can ask
     to use the computer, and that each session needs their approval
     here;
   - that what the agent reads on the computer goes to the session's log
     and to the model, and in an organization's conversation to that
     organization.
5. **The host policy file** is written (below).
6. **The environment** is created at Cella as the person, with the Cella
   token the CLI already mints: named
   `dev-` and 16 random lowercase base32 characters, `mode: worker`,
   `isolation: process`, `workers.max: 1`, the person's tenant labels and
   the device kind. `platformd` records the device on the allow, and the
   command then sets its name with `PATCH /v1/account/devices/{id}`.
7. **The key** is minted with a `ttl` of 30 days and stored in the
   keychain (below).
8. **Serve.** `worker.Run` with the `local` driver built from the host
   policy, this command's `HostPolicy` and its `Record`. The driver's
   `DataDir`, where each sandbox's scratch directory lives, is
   `$XDG_CACHE_HOME/latere/devices/<environment>` (`~/Library/Caches` on
   macOS, `~/.cache` on Linux).

While it serves, the terminal shows one status line (online, the
number of sessions using the computer) and one line per operation:
the time, the session, and the command or path.

Ctrl-C, or any other end of `connect`, stops every sandbox on the
computer before the command exits (Cella spec 082), so no background
command an agent started keeps running after the person stopped
serving.

### The host policy file

`~/.config/latere/device.yaml`, mode 0600, beside the login token:

```yaml
device: dev-01jaq7v3k9m2wxyz  # set when the computer connects
name: MacBook Pro
folders:
  - path: ~/code/site
    write: true
  - path: ~/notes
    write: false
network:
  mode: allowlist            # allowlist | none
  hosts: [github.com, codeload.github.com, registry.npmjs.org, pypi.org,
          files.pythonhosted.org, proxy.golang.org, sum.golang.org,
          crates.io, static.crates.io]
read_paths: [~/.gitconfig]   # read-only files or toolchain folders, never on the deny table
remembered_triggers: []      # {trigger, agent_version, until}, scheduled triggers only
```

The file is the computer's say: the platform never writes it, and the
command reads it at each start. A change takes effect at the next
`connect`. The folder rules are Cella's local driver's (no `/`, no home
directory itself, no system directory). The network is `allowlist` or
`none`: the local driver offers no open network. (Amended 2026-10-22: or
`any`, below.)

The command passes the driver more denied paths of its own, in `Deny`:

- its configuration directory (`~/.config/latere`: the login token, this
  file, the key fallback) and its state directory
  (`$XDG_STATE_HOME/latere`: the activity log and the lock), but not the
  cache directory that holds the sandboxes;
- the OS keychains (`~/Library/Keychains` on macOS,
  `~/.local/share/keyrings` on Linux);
- the browsers' profiles (on macOS under `~/Library/Application Support`
  and `~/Library/Safari`: Google Chrome, Chromium, Firefox, Microsoft
  Edge, Brave, Arc, Safari; on Linux `~/.mozilla`,
  `~/.config/google-chrome`, `~/.config/chromium`,
  `~/.config/microsoft-edge`, `~/.config/BraveSoftware`).

No session can read them, even through a granted folder that contains
them.

### Approving a session

Amended on 2026-10-19: a session the person started, and one a scheduled
trigger of theirs started, no longer ask by default. The rules below are
now the ones of `session_start: ask`; "Amended, 2026-10-19" under Outcome
has the default.

This command's `HostPolicy.Admit` asks in the terminal:

```
A session wants to use this computer.
  agent    Site helper (agt_01J..., version 4)
  session  ses_01J...
  started  by a person
  folder   ~/code/site (read-write)
Allow this session? [y]es / [n]o
```

For a trigger's session, `started` reads `by trigger trg_01J... (on a
schedule)` or `(on delivered events)`. The facts come from the create's
labels and the agent's name from its annotation, which Topos sets (Topos
spec 080). A compromised platform could forge them, so the approval protects
the moment and not the label: the person knows whether they just
started something.

A session the person started always asks. Nothing remembers an agent
across sessions: a stolen account could otherwise start a fresh session
of a remembered agent and run on the computer with no one asked.

The one thing the computer may remember is **a scheduled trigger** the
person owns, so it can run unattended. When the create's labels name a
trigger that fires on a schedule, the prompt adds `[r]emember this
trigger for 30 days`. The entry in `remembered_triggers` holds the
trigger's id, the agent version and an expiry, and a later fire is
admitted without asking only when all three still match. A trigger
whose agent was changed, or that fires on delivered events (mail, a
webhook: input anyone with its address can send), always asks. The
prompt says that remembering lets that trigger run here with no one
asked, and `latere devices policy` lists and forgets entries.

When no terminal is attached, a create that is not a remembered trigger's
is refused with "no one is here to approve on this computer". A create
the person does not answer is refused by Cella after its hold time
(`host_timeout`).

`HostPolicy.Check` allows every other operation. Per-command rules are
not in this spec.

### The activity log

`$XDG_STATE_HOME/latere/devices/<environment>/activity.ndjson`
(`~/.local/state` by default): one line per operation from `worker`'s
`Record`, which carries no file content and no output. It rotates at 10
MB, keeping five files. It is the computer's own account of what it was
asked to do, independent of the platform's records.

### The key

The environment key is kept in the OS keychain under the service
`latere-cli device key`, account the environment id, as the model key
is (spec 006). Where no keychain is available, it falls back to
`device-keys.json` at 0600 beside the token file, and says so at every
start.

At each `connect`, a key older than seven days is replaced: a new one is
minted, the worker registers with it, and the old one is revoked. When
the saved login has expired, which revokes nothing, the current key
serves until it expires, and then the command stops with "sign in again
with `latere login`".

`latere logout` on a connected computer says that signing out removes the
computer from the person's devices, and asks. On a yes it removes the
device itself with `DELETE /v1/account/devices/{id}`, deletes the key, and
only then revokes the login. A removal the platform refuses leaves the
person signed in. Removal does not depend on auth telling the platform
that a sign-in ended (the owner's decision of 2026-10-18 that auth stays a
standard OpenID Connect provider).

### Answers from the platform

| Answer | What the command does |
|---|---|
| `serve_refused`, `key_revoked`, or the environment is gone | stops with "this computer was removed from your devices", deletes the key, keeps the host policy and the activity log |
| `worker_limit` | stops with a warning that another computer is serving with this device's key right now, and offers `latere devices remove` |
| `worker_replaced` | stops with a warning that another process connected with this device's key and took over, and offers `latere devices remove` if that was not the person |
| `device_personal_only`, `sign_in_required` | stops with the command that fixes it |
| `device_limit` | stops with the refusal's own words, which name the limit, and `latere devices list` |
| a lost connection | reconnects with Cella's backoff and shows offline meanwhile |

### One engine, driven through a control socket

Everything this command does on the computer (the host policy, the
approvals, the activity log, the key and its rotation, the lock, stopping
every sandbox on exit) is one engine. `connect` runs the engine with a
terminal client in the same process. `serve` runs it with none, and a
desktop application, a background service or a second terminal drives
it through a local control socket. No other program reimplements the
engine or the worker protocol: they are clients of this one.

**The socket.** A Unix domain socket at
`$XDG_STATE_HOME/latere/devices/control.sock`, mode 0600, in a directory
mode 0700. The engine accepts a connection only from a process of its
own user, by the peer's credentials (`SO_PEERCRED` on Linux,
`getpeereid` on macOS). The state directory is on the driver's `Deny`
list, so no session's command can reach the socket, even though it runs
as the same user.

**The local API.** HTTP with JSON over the socket, versioned under
`/v1`, so a desktop application and the CLI it bundles can differ by a
release:

| Route | What it does |
|---|---|
| `GET /v1/status` | the device, online or offline, the sessions using it, the engine's version |
| `GET /v1/activity?follow=1` | the activity log, as it is written |
| `GET /v1/approvals?follow=1` | the creates waiting for the person, with the facts the prompt shows |
| `POST /v1/approvals/{op}` | `{"answer": "allow" \| "deny" \| "remember"}`; `remember` only for a scheduled trigger |
| `POST /v1/pause`, `POST /v1/resume` | disconnects the worker, stopping every sandbox, and connects it again |
| `POST /v1/reload` | reads the host policy file again and registers anew |

Under `serve`, a create waits for an answer from any attached client
until Cella's hold time (`host_timeout`); with no client attached it is
refused at once with "no one is here to approve on this computer",
unless it is a remembered trigger's. The first answer wins, and every
client sees the others' answers.

### Upgrades while connected

Auto-upgrade runs at the start of a command, before anything connects,
and never replaces the binary of a running `connect` or `serve`. A long
one that finds a newer release says "an upgrade is available; restart to
apply" once a day, in the terminal or in `/v1/status`.

### Out of scope

- Installing `serve` as a background service (launchd or systemd),
  notifications, and the desktop application, which are clients of the
  engine.
- Access to the whole user account (`isolation: none`).
- Keys bound to the Secure Enclave or the TPM.
- Windows.

## Verification

1. `TestConnectRefusesRoot`, `TestConnectRefusesOrgContext` and
   `TestConnectRefusesUnverifiedBuild`.
2. `TestConnectScreenNeedsYes`: nothing is created before a typed `yes`,
   and the screen lists the folders, the network and the log notice.
3. `TestConnectCreatesTheEnvironment`: against a stub Cella, the create
   carries a `dev-` name, `mode: worker`, `workers.max: 1`,
   `isolation: process` and the labels, the name is then set at
   `/v1/account/devices`, and the key lands in a fake keychain, never in
   a log.
4. `TestApprovePrompt`: `y` admits once and `n` refuses with the
   person's words; a session the person started is never remembered;
   `r` is offered only for a scheduled trigger and remembers its id,
   agent version and expiry; a remembered trigger is admitted without a
   prompt until the agent version changes or the entry expires; a trigger
   on delivered events always asks; and with no terminal anything not
   remembered is refused.
5. `TestOwnPathsDenied`: with a granted folder that contains
   `~/.config/latere`, a stage cannot read the login token or the host
   policy file, and the sandboxes under the cache directory still run.
6. `TestStopEndsBackgroundCommands`: a background command an agent
   started is gone after Ctrl-C.
7. `TestSecondConnectRefusedLocally`: a second `connect` on the same
   computer stops at the lock without reaching the platform.
8. `TestActivityLogHasNoContent` and `TestActivityLogRotates`.
9. `TestKeyRotation`: a key older than seven days is replaced and the
   old one revoked after the new registration.
10. `TestRemovedDeviceStops`: `serve_refused` and `key_revoked` stop the
   command and delete the key; `worker_replaced` stops it and keeps the
   key.
11. `TestLogoutWarnsOnAConnectedComputer`: a no keeps the login and the
   device; a yes removes the device at the platform and deletes its key
   before the login is revoked; a removal the platform refuses keeps the
   login.
12. `TestControlSocket`: the socket is 0600 in a 0700 directory; a
   connection from another user is refused; a stage's command cannot
   open it; an approval answered over it admits the create; with no
   client attached, `serve` refuses an unremembered create at once; and
   two clients see each other's answers.
13. An end-to-end run in CI on Linux: `latere devices connect` against a
   real `cellad` with an embedded worker on the `local` driver, a stub
   authorizer allowing the owner, a sandbox on a granted folder, an
   approved exec, and a removal that stops the command.

## Outcome

Built on 2026-10-18, not released, on Cella v0.17.2. Every verification
item has its test, and items 5, 12 (its stage half) and 13 ran against the
real `srt` on macOS, and since 2026-10-19 on Linux in CI as well (below).
The live run waits for platform spec 220 and Topos spec 080 to be released.

| Item | Test | Where |
|---|---|---|
| 1 | `TestConnectRefusesRoot`, `TestConnectRefusesOrgContext`, `TestConnectRefusesUnverifiedBuild` | `internal/commands/devices_test.go` |
| 2 | `TestConnectScreenNeedsYes` | same |
| 3 | `TestConnectCreatesTheEnvironment` | same |
| 4 | `TestApprovePrompt` | `internal/devices/terminal_test.go` |
| 5 | `TestOwnPathsDenied`, skipped where `srt` is missing | `internal/devices/engine_test.go` |
| 6 | `TestStopEndsBackgroundCommands` | same |
| 7 | `TestSecondConnectRefusedLocally` | `internal/commands/devices_test.go` |
| 8 | `TestActivityLogHasNoContent`, `TestActivityLogRotates` | `internal/devices/engine_test.go`, `activity_test.go` |
| 9 | `TestKeyRotation` | `internal/devices/engine_test.go` |
| 10 | `TestRemovedDeviceStops` (at registration and on the stream) | same |
| 11 | `TestLogoutWarnsOnAConnectedComputer` | `internal/commands/logout_device_test.go` |
| 12 | `TestControlSocket`; `TestStageCannotReachTheControlSocket`, skipped where `srt` is missing | `internal/devices/control_test.go` |
| 13 | `TestDevicesConnectEndToEnd` | `cmd/latere/devices_e2e_test.go` |

The gate's runners have no `srt`. There, the end-to-end run and the
engine's tests run the local driver under a stand-in `srt` that runs each
command unconfined, so the whole flow (connect, approve, exec, removal) is
proved on every runner. Confinement is proved where the real `srt` is: a
developer's Mac, and the CI job "devices under the real sandbox on Linux",
added on 2026-10-19, which installs `srt` with bubblewrap, socat and
ripgrep on Ubuntu and runs the engine's tests, the command's device tests
and the end-to-end run against a real `cellad`. That job fails when one of
them skips or falls back to the stand-in. Its first run found two faults in
the tests, none in the CLI or in Cella: `TestStopEndsBackgroundCommands`
read the pid a command reported for itself, which under bubblewrap is the
pid inside the sandbox's own PID namespace, so it now finds each command on
the computer by its command line; and `TestTerminalShowsTheEngine` could
let a `y` typed with nothing waiting answer the next prompt, a race in the
test that a slower runner shows.

Since 2026-10-21 the engine is Cella's `latere.ai/x/cella/device`, from
Cella v0.20.0, which Latere Studio embeds too, and `internal/devices` is
gone: the engine's tests in the table (items 4 to 6, 8 to 10 and 12) are
Cella's, run under the real `srt` in Cella's CI, and the terminal client
is `internal/deviceterm`, with its own tests. The command embeds the engine
with the Topos label keys, the platform's audience and its three own
refusals (`internal/commands/devices_latere.go`), and the files on disk
are the same, so a computer connected before is served as it is. From
the Go SDK's v0.3.0, which Latere Studio imports too, every Latere value
among them is the SDK's: the label keys, the device kind label, the
platform's audience, the reader of a handed token's claims, and the
platform's device and labels routes. `devices_latere.go` maps them into
the engine's types.

Where the build differs from the design, or adds to it:

- **No fresh sign-in, no `sid`, no `device_name`.** On 2026-10-18 the owner
  decided auth stays a standard OpenID Connect provider and auth spec 097
  was reverted. Connecting takes the saved login in personal context, and
  `latere logout` removes the device itself before it revokes the login
  (Design now says so). `logout --yes` answers its question.
- **One engine in `internal/devices`.** The engine, the terminal client
  (`devices.Terminal`, which prints the status, the operations and the
  prompt), the control socket and the lock live there; `internal/commands`
  wires them (`devices.go`, `devices_connect.go`, and `devices_records.go`
  for the platform's routes, which builds on every platform for `logout`).
  There is no `devices_approve.go`. On Windows `latere devices` refuses
  with `device_unsupported`.
- **The create declares a capacity,** `spec.capacity.sandboxes` 8, one
  sandbox per session: Cella refuses an environment without one, which the
  end-to-end run found.
- **The key is minted when the engine starts.** Enrollment ends with the
  name; the engine mints a key wherever none is kept, so a computer that
  lost its key mints one at the next start. A key that expires while the
  engine serves is renewed when the person's sign-in still works, and the
  engine stops with `device_sign_in_expired` when it does not.
- **A key refused at registration asks about the device.** Cella retries a
  registration refused with 401, so the engine asks, as the person, whether
  the environment is still theirs: gone or refused as removed stops the
  engine as removed (the device was removed while the computer was away);
  still theirs gets a new key.
- **Removal keeps the host policy's folders.** A refusal that removes the
  device deletes the key and clears `device` in the file, keeping folders,
  network and remembered triggers, so the next connect proposes them.
  `latere devices remove` on this computer deletes the file, as Design
  says; `logout` clears `device` and keeps it.
- **The approval rules in practice.** `n` refuses with "the owner of this
  computer refused this session". With no client attached a create is
  refused at once with "no one is here to approve on this computer", and
  when the last client detaches every create still waiting is refused the
  same way. Each answer, and each admission of a remembered trigger, is a
  line of the activity log of type `approval`. The facts come from the
  labels; the agent's name from the annotation reaches the worker from
  Cella v0.17.0 on, with the agent ref shown where there is none.
- **The terminal.** The status line is printed when it changes rather than
  redrawn in place. Operations that only read the sandbox's state for the
  control plane (Inspect, List, Watch, Touch) are logged, not printed.
- **The activity log** keeps the current file and five older ones.
  `latere devices activity --device ID` reads the log of a device the
  computer was before, and `--json` prints each line as it is kept.
- **`LATERE_DEVICE_KEYS_FILE`** keeps the key in a file and never asks the
  keychain, for a computer whose keychain needs a person to unlock it; the
  end-to-end run uses it, so no test reaches the macOS keychain. That file
  is denied to sessions like the configuration directory.
- **Flags on a connected computer are refused** with
  `device_already_connected`: the host policy file is where a change is
  made. Without `--folder` the current directory is shared, `--host`
  replaces the default list, and `--network none` empties it.
- **The local API.** `POST /v1/approvals/{op}` takes an optional `client`,
  the name the other clients read beside the answer; `op` is an approval id
  the engine mints. `/v1/status` answers the device, `state` (`starting`,
  `online`, `offline`, `paused`), `sessions`, `waiting`, `version` and
  `upgrade`. A reload of a file that names another device, or that the
  driver's rules refuse, is refused and the engine keeps serving. A socket
  path longer than a socket address holds (103 bytes) is refused with
  `device_control_unavailable`, naming `XDG_STATE_HOME`.
- **Online and offline** were read from the worker's registration answers
  and from its log line when the stream ends. Since Cella v0.18.0 they are
  read from `worker.Options.State` (amended on 2026-10-19, below).
- **`device_limit`** keeps its fixed sentence; the next step names how many
  devices the person has and the limit `GET /v1/account/devices` answers.
- **Three Cella gaps this build found, each fixed in Cella:** a worker's
  host policy saw no annotations, so the agent's name could not reach the
  prompt (v0.17.0); a stage could not stat its own output when the
  sandboxes sit under the home directory, which broke `cat` on macOS
  (v0.17.1); and an exec naming a directory in the person's folder was
  refused by Cella's exec route (v0.17.2). The end-to-end run covers all
  three.

### Live, 2026-10-19

Released in v0.22.1 (v0.22.0's release stopped on a test that read the
sandbox's state before the worker had stopped it; the test now waits for
it). Checked on production from a Mac: `latere devices connect` with a
granted folder and no network came online; a hosted session named the
device and folder; its bash call was confirmed; the computer asked for its
approval with the agent's name, the session and the folder, and was
answered through the control socket; the command read a file in the
folder, wrote one there and ran `uname` (Darwin); the activity log held
every operation; `latere devices remove` ended `connect` with
`device_removed` and left no key in the keychain. Two small things seen on
the way: `latere devices list` right after a connect can show the device
offline for a moment, and the activity log words a missing file as
"environment not found".

### Amended, 2026-10-19: how a session starts

The owner decided on 2026-10-19 that a device starts the person's own
sessions without asking on the computer. Approval of what a session does
stays where it is: each command that changes something is approved in the
chat through Topos's confirm list, unless the person approved a matching
one before. Only the prompt before a session starts changes. This
supersedes "A session the person started always asks" in Design, and the
reason given there (a stolen account starting a fresh session of a
remembered agent): under the default, someone with the person's sign-in
reaches the granted folders, still confined to them, and the connect
screen says so.

- **The host policy file** gains `session_start: allow | ask`. A new
  connection writes `allow` unless `connect --session-start ask` says
  otherwise, and a file without the field, as every file written before
  it, reads as `allow`; any write of the file spells it out. Any other
  value makes the file invalid.
- **Under `allow`** a create with no trigger label, and one whose trigger
  fires on a schedule, is admitted with no one asked. A trigger that fires
  on delivered events, and one whose create does not name how it fires,
  asks as before; with no one attached it is refused with "no one is here
  to approve on this computer". Under `ask`, nothing changed.
- **Read at each create.** `session_start` is read from the file at each
  create, as the remembered triggers already were, so a change applies to
  the next session with the engine serving. A file that cannot be read, or
  that names another device, asks.
- **Every admitted session is told**, asked or not, remembered or not: one
  line in the terminal (`Site helper (ses_..., started by a person) is
  using ~/code/site (read-write).`), a line of the activity log of type
  `approval` (the answer `session_start_allow` when no one was asked), and
  a `started` event on `GET /v1/approvals?follow=1`, with the facts the
  prompt would show, for a desktop client to notify of.
- **Switching.** Flags on a connected computer are still refused with
  `device_already_connected`. `latere devices policy --session-start allow
  | ask` writes the field instead, and the refusal's next step names it. At
  every start the engine prints how sessions start and the command that
  switches, since a computer connected before this change never sees the
  connect screen again.
- **`latere devices pause` and `resume`** are new: clients of the control
  socket's `POST /v1/pause` and `/v1/resume`, which existed only on the
  socket. Pause stops every command a session started and keeps sessions
  out while `connect` runs. With nothing serving they stop with
  `device_not_serving`.
- **The connect screen** states the mode, that anyone who can start
  sessions as the person reaches the granted folders, still confined, that
  commands are still approved in the chat, and the command that switches.

| Item | Test | Where |
|---|---|---|
| The field: default, a file without it, writes, a bad value | `TestSessionStartInTheFile`, `TestStartsWithoutAsking`, `TestPolicyProblems` | `internal/devices/policy_test.go` |
| Allow: a person's session and a scheduled trigger start unasked, events and unnamed triggers ask, no one attached refuses, a switch applies at once | `TestSessionStartAllow` | `internal/devices/terminal_test.go` |
| Every admission is told: the line, the activity log, the stream | `TestSessionStartAllow`, `TestApprovePrompt`, `TestControlSocket` | `internal/devices/` |
| The screen and the start notice | `TestConnectScreenWording`, `TestStartedLineAndNotice`, `TestConnectScreenNeedsYes` | `internal/devices/terminal_test.go`, `internal/commands/devices_test.go` |
| `--session-start` on connect and policy | `TestConnectProposesSessionStart`, `TestConnectSessionStartFlag`, `TestDevicesPolicy`, `TestConnectCreatesTheEnvironment` | `internal/commands/` |
| `pause` and `resume` | `TestControlPost`, `TestDevicesPauseAndResume` | `internal/devices/control_test.go`, `internal/commands/devices_session_start_test.go` |
| End to end: a session unasked, then `--session-start ask` and an approval over the socket | `TestDevicesConnectEndToEnd` | `cmd/latere/devices_e2e_test.go` |

### Amended, 2026-10-19: the worker's state

On Cella v0.18.0 the engine learns whether the computer is online from
`worker.Options.State`, the typed hook the worker calls on each change of
its connection, in place of a transport that read each registration's
answer and a logger that matched the line the worker logs when its stream
ends.

- **Online** is a State with `Connected`: the registration taken and the
  stream open. It was the registration alone before. A key this start
  replaced is revoked then, as before.
- **Offline**, while the worker reconnects, says why: a key refused with
  401 (a `worker.RegistrationError`), which asks whether the device still
  exists as before; another answer to the registration, by its status; a
  stream that ended; or a control plane never reached.
- **The last State**, as `worker.Run` returns, changes nothing: a pause, a
  reload, a stop and a refusal are answered by the engine as before.
- The worker's own log lines are discarded, as the logger that matched them
  discarded the rest; the person reads the engine's events.
- `TestEngineFollowsTheWorkerState` runs a real worker against the stub
  control plane through a stream that ends and a registration answered 503,
  and `TestWorkerStateReads` holds each State to the state and detail it
  reads as.

Cella v0.18.0 also writes the environment's status at registration and
words a missing path "not found: <path>", which is how the activity log now
reads one, in place of "environment not found" in the live check above.

### Amended, 2026-10-22: the network any, refused hosts, `latere devices allow`

On Cella v0.21.0 a device's network can be `any`: any public host but
those `network.blocked` names, never this computer, its networks, or an
address that is not public. With it, everything readable in a granted
folder can be sent to any public host, so it is never a default and
never proposed at a connect.

- **`latere devices policy --network allowlist | none | any`**, with
  `--host` and `--block`, each naming its whole list. The result is
  checked as the engine checks a policy before anything is written. A
  switch of mode starts each list from what the new mode reads of the
  old: an allowlist from the default hosts, `any` from the entries naming
  a port beyond 443 and 80. Choosing `any` shows the network facts of the
  connect screen, the sentence among them, and takes the same typed yes,
  or `--yes`; a refusal is `device_network_not_confirmed`. Where the
  driver cannot enforce `any` (srt, and a binary whose main does not run
  hostsandbox's launcher) it is `device_network_unenforceable`, before
  anything is asked. The policy's output shows the network facts.
  `connect --network any` is refused with the command that sets it.
- **Refused hosts.** The terminal client prints each `network_refused`
  event as one line, the session (or the sandbox where the plane names
  none), the host and the port, with the two commands that allow it, and
  does not print the refusal's activity line beside it.
- **`latere devices allow HOST[:PORT] (--session ID | --save)`** rather
  than flags on `policy`: the two answer the same refusal and differ in how
  long the answer lasts, and `--session` changes no file. `--session` is
  `POST /v1/sessions/{session}/hosts`; `--save` is `POST /v1/policy/hosts`,
  or, with nothing serving, `Engine.SaveHost` on an engine that does not
  run, so the file is changed and checked exactly as the serving engine
  would. A host alone is port 443.
- **Older and newer binaries.** A latere before this release refuses a
  file whose network is `any` (`blocked` is an unknown field to it), so
  every write that leaves `any` says to upgrade. A file naming a field this
  version does not know is `device_policy_newer`, whose step is `latere
  upgrade`.

| Item | Test | Where |
|---|---|---|
| The flags, each list on a switch, refusals before any write, unenforceable `any` | `TestDevicesPolicyNetworkFlags` | `internal/commands/devices_network_test.go` |
| The typed yes and `--yes`, the policy's network output, the upgrade note | `TestDevicesPolicyNetworkAnyAsksForYes` | `internal/commands/devices_network_test.go` |
| `connect --network any` refused | `TestConnectRefusesNetworkAny` | `internal/commands/devices_network_test.go` |
| A newer file says upgrade | `TestDevicesNewerPolicyFileSaysUpgrade` | `internal/commands/devices_network_test.go` |
| `allow` through the socket: routes, bodies, refusals | `TestDevicesAllowThroughTheEngine` | `internal/commands/devices_allow_test.go` |
| `allow` with nothing serving: allowlist, a block lifted, a wildcard kept | `TestDevicesAllowWithNothingServing` | `internal/commands/devices_allow_test.go` |
| The activity lines | `TestDevicesActivityShowsRefusedHosts` | `internal/commands/devices_allow_test.go` |
| The refused line and its commands | `TestTerminalShowsRefusedHosts`, `TestRefusedLines` | `internal/deviceterm/terminal_test.go` |
| End to end on macOS: a refusal printed, allowed for the session, saved, logged; `any` taken by the built binary | `TestDevicesSessionCommandsRunUnderSeatbeltEndToEnd`, `TestDevicesNetworkAnyUnderSeatbeltEndToEnd` | `cmd/latere/devices_seatbelt_e2e_test.go` |
