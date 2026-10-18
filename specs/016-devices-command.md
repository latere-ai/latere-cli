---
title: "`latere devices`: connect this computer as one of the person's environments and serve it in the foreground through Cella's embedded worker and local driver, the host policy file of granted folders, network and remembered scheduled triggers, the person approving each session in the terminal or through the engine's control socket, `latere devices serve` for a desktop application or a background service, an activity log of every operation, the environment key in the keychain and rotated, and list, rename, remove, policy and activity"
status: in-progress
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
  - "CROSS-REPO: latere-ai/auth specs/097-a-sign-in-names-its-device.md: `device_name` at sign-in and `sid` on the token"
affects:
  - internal/commands/devices.go (new), devices_connect.go (new), devices_approve.go (new)
  - internal/devices/ (new: the host policy file, the key store, the activity log)
  - internal/commands/auth.go (`device_name` at sign-in; `logout` warns on a connected computer)
  - internal/commands/root.go (the `devices` group)
  - go.mod (`latere.ai/x/cella` at the release with `worker` and `runtime/local`)
  - docs/devices.md (new), README.md, CHANGELOG.md
effort: large
created: 2026-10-18
updated: 2026-10-18
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
3. **A sign-in that names the computer.** When the saved login has no
   `sid`, the command signs in again with the device-code grant, sending
   `device_name` (the computer's name, or `--name`).
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
   token the CLI already mints (it carries the sign-in's `sid`): named
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
`none`: the local driver offers no open network.

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

`latere logout` on a connected computer revokes the sign-in that
connected it, which removes the device (auth spec 097, platform spec
220). It says so and asks before it signs out.

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
11. `TestLogoutWarnsOnAConnectedComputer`.
12. `TestControlSocket`: the socket is 0600 in a 0700 directory; a
   connection from another user is refused; a stage's command cannot
   open it; an approval answered over it admits the create; with no
   client attached, `serve` refuses an unremembered create at once; and
   two clients see each other's answers.
13. An end-to-end run in CI on Linux: `latere devices connect` against a
   real `cellad` with an embedded worker on the `local` driver, a stub
   authorizer allowing the owner, a sandbox on a granted folder, an
   approved exec, and a removal that stops the command.
