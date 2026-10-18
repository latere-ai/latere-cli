# Devices

`latere devices connect` connects your own computer to Latere, so the
sessions you start can work on it: run commands and read and edit files in
the folders you choose, on your own hardware, with the tools you already
have installed. The computer shows up as one of your environments in the
console and in the chat.

Your computer has the last word. Each session asks for your approval on the
computer before it starts, sessions reach only the folders you share, and
the computer keeps its own log of everything it was asked to do.

## What you need

- macOS or Linux. Windows is not supported.
- The sandbox runtime `srt`, which confines every command a session runs:

  ```sh
  npm install -g @anthropic-ai/sandbox-runtime
  brew install ripgrep                                # macOS
  sudo apt-get install -y bubblewrap socat ripgrep    # Debian, Ubuntu
  srt echo ok
  ```

- A `latere` installed from a release, which verifies its own upgrades. A
  build that cannot is refused.
- Your own user account. `latere devices` refuses to run as root.
- `latere login`, in your personal context. A device is personal: if you
  are signed in to an organization, run `latere org --personal` first.

## Connect this computer

```sh
cd ~/code/site
latere devices connect
```

The first time, `connect` shows what you are about to allow and asks you to
type `yes`:

- the folders sessions may use, each read-write or read-only. Everything in
  them is readable to the agent, a project's `.env` file included;
- the network: the hosts commands may reach, or none. A host that accepts
  writes, such as a git host, can carry data out;
- that only sessions you start, and triggers you own, can ask to use the
  computer, and that each one needs your approval there;
- that what the agent reads on the computer goes to the session's log and
  to the model, and in an organization's conversation to that organization.

After `yes`, it writes the host policy file, connects the computer as one of
your devices, and starts serving it. It runs in the foreground until you
press Ctrl-C. The next time, `latere devices connect` serves the device this
computer already is, with no questions.

| Flag | What it does |
|---|---|
| `--folder PATH` | A folder sessions may use, read-write. Add `:ro` for read-only. Repeat it for more. Without it, the current directory is shared. |
| `--network allowlist` or `--network none` | Whether commands reach the hosts on the list, or no network at all. The default is `allowlist`. |
| `--host H` | A host on the allowlist. Repeat it for more. Without it, the list holds the common git and package hosts: `github.com`, `codeload.github.com`, `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `proxy.golang.org`, `sum.golang.org`, `crates.io` and `static.crates.io`. |
| `--name N` | The computer's name among your devices. Without it, the computer's own name. |

There is no open network: the sandbox allows named hosts only. The folders
follow the sandbox's rules: not `/`, not your home directory itself, not a
system directory such as `/usr` or `/Library`, and nothing another user
owns.

## Approving a session

When a session asks to use the computer, `connect` asks you:

```
A session wants to use this computer.
  agent    Site helper (agt_01J..., version 4)
  session  ses_01J...
  started  by a person
  folder   ~/code/site (read-write)
Allow this session? [y]es / [n]o
```

Type `y` to allow it or `n` to refuse it. Allowing a session allows that
session once: the next session asks again, even for the same agent, so a
stolen account cannot start a fresh session of an agent you trusted and run
on your computer with no one asked.

A session started by a trigger reads `started  by trigger trg_... (on a
schedule)` or `(on delivered events)`. A trigger that fires on a schedule
can be remembered, so it can run while you are away: the prompt then offers
`[r]emember this trigger for 30 days`. A remembered trigger runs without
asking until its agent changes or 30 days pass. A trigger that fires on
delivered events, such as mail or a webhook, always asks, since anyone with
its address can send it input. `latere devices policy` lists the triggers
you remembered, and `latere devices policy --forget TRIGGER` forgets one at
once, even while the computer is serving.

If no one can answer, because `connect` is not running in a terminal and no
other client is attached, a session is refused with "no one is here to
approve on this computer", unless it is a trigger you remembered. A session
you do not answer is refused after ten minutes.

## While it serves

`connect` prints one line whenever the computer goes online or offline or
the number of sessions using it changes, and one line per command or file a
session touches:

```
Online. 1 session is using this computer.
09:03:22  ses_01J...  exec  git status
09:03:25  ses_01J...  write  ~/code/site/main.go
```

When the connection drops, it reconnects on its own. Ctrl-C, or anything
else that ends `connect`, stops every command a session started on the
computer before it exits, background commands included.

## What a session can reach

A session works in one of the folders you shared, read-write or read-only
as you shared it. Nothing else in your home directory is readable. Some
paths are denied even inside a folder you shared:

- the credentials the sandbox always denies: `~/.ssh`, `~/.aws`,
  `~/.config/gcloud`, `~/.kube`, `~/.netrc`, `~/.gnupg`,
  `~/.docker/config.json`, `~/.git-credentials`, `~/.npmrc`, `~/.pypirc`
  and `~/.env`;
- `latere`'s own configuration (your login token, the host policy file,
  the key file) and its state (the activity log, the control socket);
- your keychains, and your browsers' profiles: Chrome, Chromium, Firefox,
  Edge, Brave, Arc and Safari.

A secret file you did not name inside a shared folder, such as a project's
own `.env`, is part of what you shared. The directories on your `PATH` are
readable so your tools run, and each sandbox has its own scratch home and
temporary directory under `latere`'s cache directory.

## The host policy file

`~/.config/latere/device.yaml` is your computer's own say over what
sessions may use. The platform never writes it.

```yaml
device: dev-01jaq7v3k9m2wxyz   # set when the computer connects
name: MacBook Pro
folders:
  - path: ~/code/site
    write: true
  - path: ~/notes
    write: false
network:
  mode: allowlist              # allowlist or none
  hosts: [github.com, codeload.github.com, registry.npmjs.org]
read_paths: [~/.gitconfig]     # files or toolchain folders commands may read where they are
remembered_triggers: []        # each: trigger, agent_version, until
```

Edit it to share another folder, change the network, or add a read path,
then stop `connect` and run it again: a change takes effect at the next
connect. `latere devices policy` prints the file and checks it against the
sandbox's rules. `read_paths` suits a toolchain outside every `PATH`
directory, such as `~/.rustup`, and cannot name a denied path.

## The activity log

Every operation a session asks of the computer, and every approval, is
written to `~/.local/state/latere/devices/<device>/activity.ndjson`, one
JSON object per line. It holds the command or the path, never a file's
content or a command's output. It is the computer's own account, kept
whatever the platform records. At 10 MB the file is rotated, and five older
files are kept.

```sh
latere devices activity               # the whole log
latere devices activity -f            # and follow it
latere devices activity --since 24h   # from a day ago
latere devices activity --json        # one JSON object per line
```

## The key

The computer serves with a key of its own, kept in your keychain (the
macOS keychain, or the Secret Service on Linux) under `latere-cli device
key`. Where no keychain answers, it is kept in
`~/.config/latere/device-keys.json`, readable by you alone, and `connect`
says so each time it starts. Set `LATERE_DEVICE_KEYS_FILE` to keep it in a
file of your choice and never ask the keychain, for a computer whose
keychain needs a person to unlock it.

A key lasts 30 days. Each `connect` replaces a key older than seven days
and revokes the old one once the new one works. If your sign-in has
expired, the current key serves until it expires, and then `connect` stops
with "sign in again with `latere login`".

## Your devices

```sh
latere devices list                           # your devices; * marks this computer
latere devices rename "Work laptop"           # rename this computer
latere devices rename "Desktop" dev-01j...    # or another of yours
latere devices remove                         # remove this computer
latere devices remove dev-01j...              # or another of yours
```

Removing a device is final: sessions can no longer use it, every command a
session started there stops, and its key stops working. On this computer,
`remove` also deletes the key and the host policy file, and keeps the
activity log. Connecting the computer again makes a new device. You can also
remove a device from the console or the chat.

`latere logout` on a connected computer removes it from your devices before
it signs out. It says so and asks; `latere logout --yes` answers for you. If
the device cannot be removed, you stay signed in.

## For a desktop application

`latere devices serve` serves the computer with no one to answer in the
terminal. A desktop application, a background service or a second terminal
drives it through a local socket, `~/.local/state/latere/devices/control.sock`,
which only processes of your own user can open and no session can reach.
It speaks HTTP with JSON, versioned under `/v1`:

| Route | What it does |
|---|---|
| `GET /v1/status` | The device, `state` (`starting`, `online`, `offline` or `paused`), the sessions using it, the creates waiting, the version, and an upgrade when one was found while serving |
| `GET /v1/activity?follow=1` | The activity log as newline-delimited JSON, and with `follow=1` each line as it is written. `since=` takes a duration such as `1h` or a time |
| `GET /v1/approvals?follow=1` | The sessions waiting for an answer, and with `follow=1` every one asked, answered or withdrawn. While a client holds this stream, a session waits for an answer |
| `POST /v1/approvals/{id}` | `{"answer": "allow" \| "deny" \| "remember", "client": "Your app"}`. `remember` only for a trigger that fires on a schedule. The first answer wins, and every client sees it |
| `POST /v1/pause`, `POST /v1/resume` | Disconnect, stopping every command on the computer, and connect again |
| `POST /v1/reload` | Read the host policy file again and connect anew |

Every refusal is `{"error": {"code", "message", "details": {"detail"}}}`.

## When a command is refused

| Code | What to do |
|---|---|
| `device_runs_as_root` | Run `latere devices` as yourself, not with `sudo`. |
| `device_unverified_build` | Install `latere` from https://latere.ai. |
| `device_already_served` | Another `connect` or `serve` is running on this computer; stop it first. |
| `device_org_context` or `device_personal_only` | Run `latere org --personal`, then connect again. |
| `device_no_sign_in` or `sign_in_required` | Run `latere login`, then connect again. |
| `device_key_refused` | Sign in with `latere login`, not with a pasted key. |
| `device_limit` | You connected as many computers as one person may. Remove one with `latere devices remove ID`. |
| `device_sandbox_unavailable` | Install `srt` and what it needs; the detail names it. |
| `device_policy_invalid` | Fix the host policy file; `latere devices policy` shows what is wrong. |
| `device_removed` | The computer was removed from your devices. Run `latere devices connect` to connect it again. |
| `device_worker_limit` | Another computer is serving with this device's key right now. If that is not you, remove the device. |
| `device_worker_replaced` | Another process connected with this device's key and took over. If that was not you, remove the device. |
| `device_sign_in_expired` | Your sign-in and the key have both expired. Run `latere login`, then connect again. |
