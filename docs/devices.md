# Devices

`latere devices connect` connects your own computer to Latere, so the
sessions you start can work on it: run commands and read and edit files in
the folders you choose, on your own hardware, with the tools you already
have installed. The computer shows up as one of your environments in the
console and in the chat.

Your computer has the last word. Sessions reach only the folders you share,
`latere devices pause` stops every one of them at once, and the computer
keeps its own log of everything it was asked to do. The sessions you start
begin on the computer without asking there, and each one is shown as it
starts; a trigger that runs on mail or a webhook asks on the computer first.
Each command that changes something is still approved in the chat, unless
you approved a matching one before.

## What you need

- macOS or Linux. Windows is not supported.
- On macOS, nothing more: every command a session runs is confined by
  Seatbelt, which is part of macOS.
- On Linux, the sandbox runtime `srt`, which confines every command a
  session runs:

  ```sh
  npm install -g @anthropic-ai/sandbox-runtime
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
- that only sessions you start, and triggers you own, can use the computer;
  how they start there (below), and how to change it;
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
| `--host H` | A host on the allowlist. On macOS it is reached over HTTPS on port 443 and HTTP on port 80; write `H:PORT` for another port, as `github.com:22` for git over SSH. Repeat it for more. Without it, the list holds the common git and package hosts: `github.com`, `codeload.github.com`, `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `proxy.golang.org`, `sum.golang.org`, `crates.io` and `static.crates.io`. |
| `--name N` | The computer's name among your devices. Without it, the computer's own name. |
| `--session-start allow` or `--session-start ask` | Whether the sessions you start, and your triggers that run on a schedule, start without asking on the computer. The default is `allow`. With `ask`, every session asks. |

There is no open network: the sandbox allows named hosts only. The folders
follow the sandbox's rules: not `/`, not your home directory itself, not a
system directory such as `/usr` or `/Library`, and nothing another user
owns.

## How a session starts

Under the default, `session_start: allow`, a session starts on the computer
with no one asked there when you started it, or when a trigger of yours that
runs on a schedule started it. `connect` prints one line as it starts, and
the activity log and the control socket record it:

```
Site helper (ses_01J..., started by a person) is using ~/code/site (read-write).
```

This means anyone who can start sessions as you, someone who has your
Latere sign-in, can reach the folders you share. They stay confined to
those folders: never your credentials, keychains or browser profiles, and
no network beyond the hosts you listed. Each command that changes something
is still approved in the chat, unless you approved a matching one before.
`latere devices pause` stops every command a session started on the
computer at once, and keeps sessions out until `latere devices resume`.

A trigger that runs on delivered events, such as mail or a webhook, always
asks on the computer first, since anyone with its address can send it
input:

```
A session wants to use this computer.
  agent    Site helper (agt_01J..., version 4)
  session  ses_01J...
  started  by trigger trg_01J... (on delivered events)
  folder   ~/code/site (read-write)
Allow this session? [y]es / [n]o
```

Type `y` to allow it or `n` to refuse it. Allowing it allows that session
once: the trigger's next session asks again.

To be asked before every session starts, run:

```sh
latere devices policy --session-start ask     # every session asks here
latere devices policy --session-start allow   # back to the default
```

The change applies to the next session, even while the computer is serving.
`latere devices connect --session-start ask` sets it when you first connect.

Under `ask`, a session you started reads `started  by a person`, and one a
trigger started reads `started  by trigger trg_... (on a schedule)` or `(on
delivered events)`. A trigger that runs on a schedule can then be
remembered, so it can run while you are away: the prompt offers `[r]emember
this trigger for 30 days`. A remembered trigger runs without asking until
its agent changes or 30 days pass. `latere devices policy` lists the
triggers you remembered, and `latere devices policy --forget TRIGGER`
forgets one at once, even while the computer is serving.

If no one can answer, because `connect` is not running in a terminal and no
other client is attached, a session that asks is refused with "no one is
here to approve on this computer", unless it is a trigger you remembered. A
session you do not answer is refused after ten minutes.

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

To stop every session without stopping `connect`, run `latere devices
pause` from another terminal: every command a session started on the
computer stops, and no session can use it until `latere devices resume`.

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
  Edge, Brave, Arc and Safari;
- on macOS, Latere Studio's data and cache,
  `~/Library/Application Support/ai.latere.studio` and
  `~/Library/Caches/ai.latere.studio`, where the app keeps its local copy
  of your conversations, whether the app or `latere devices` serves the
  computer.

On macOS a command also cannot write, inside a folder you shared, the
files your own tools later run as code: shell startup files (`.zshrc`,
`.bashrc` and the others), `.envrc`, `.gitconfig`, `.gitmodules`,
`.mcp.json`, `*.code-workspace`, and anything in `.claude`, `.cursor`,
`.vscode` or `.idea`. In a repository it writes only what everyday git
writes, never the repository's config or hooks, so `git add`, `commit`,
`checkout`, `merge`, `rebase` and `fetch` work there and `git init`,
`clone`, `config` and `remote add` do not. Its network goes through a
proxy beside it that admits the hosts on the list and never decrypts a
connection.

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
session_start: allow           # allow or ask
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
connect. `session_start` and `remembered_triggers` are read at each session,
so a change to them applies to the next one. A file written before
`session_start` existed has no such line, and reads as `allow`. `latere devices policy` prints the file and checks it against the
sandbox's rules. `read_paths` suits a toolchain outside every `PATH`
directory, such as `~/.rustup`, and cannot name a denied path.

## The activity log

Every operation a session asks of the computer, and every session's start,
asked or not, allowed or refused, is written to
`~/.local/state/latere/devices/<device>/activity.ndjson`, one JSON object
per line. A session that started with no one asked reads `allowed without
asking: session_start is allow`. It holds the command or the path, never a file's
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
the device cannot be removed, you stay signed in. While Latere Studio or
`latere devices` in another terminal serves the computer, the device is that
program's too: `logout` signs you out, keeps the computer connected, says
so, and asks nothing. To remove it, run `latere devices remove` while you
are signed in.

## For a desktop application

`latere devices serve` serves the computer with no one to answer in the
terminal. Sessions that start without asking start as under `connect`. A desktop application, a background service or a second terminal
drives it through a local socket, `~/.local/state/latere/devices/control.sock`,
which only processes of your own user can open and no session can reach.

An application can take a computer from nothing to serving without a
terminal. On a computer that is not connected, `serve` waits in the state
`not_connected` with its socket open; the application asks for the
proposal, shows its facts, and connects with the consent it answered.
After the device is removed, `serve` waits again.

`serve --credentials client` never reads your saved sign-in: the
application hands it short-lived tokens it minted for `cella` and
`api.latere.ai` from its own sign-in, kept in memory only, and the status
says when one is needed again. The tokens must be your own, in your
personal context. Once the platform has confirmed whose device it is, a
token of another person is refused: the owner is recorded when you connect
the computer, from the terminal or through the socket, and for a device
connected before this release, the first time the platform mints the
computer's key for a person or answers their sign-in as the device's own.
Until then any person's tokens are taken, and when the platform answers
them that the device is not theirs, the computer keeps the device, drops
the tokens, and its status says, offline, that it needs the owner's
sign-in. An application signed in as someone else never makes the computer
forget your device. When the computer's key must be minted and no token is
held, `serve` waits, offline, until the application hands one. A bundled copy runs with
`LATERE_NO_UPDATE_CHECK=1`, so it never looks for a release and never
replaces itself.

The socket speaks HTTP with JSON, versioned under `/v1`:

| Route | What it does |
|---|---|
| `GET /v1/status?follow=1` | The device, `state` (`starting`, `online`, `offline`, `paused` or `not_connected`), the sessions using it, the creates waiting, the version, an upgrade when one was found while serving, and under `--credentials client` whether each token is `valid` or `needed`. With `follow=1`, the status as newline-delimited JSON each time it changes, which is how an application learns a token is needed. A follower that falls behind is dropped: its stream ends, and it reconnects and reads the status again |
| `GET /v1/connect?name=&folder=&network=&host=&session_start=` | On a computer not connected, the proposal: the `policy` filled as `connect` fills its flags (`folder` and `host` repeat; a folder is absolute or under `~`, and ends in `:ro` to be read-only), the `facts` the connect screen states, each with its `kind`, `text`, `items` and `note`, a `consent` digest of both, and the `problems` with the policy. With no `folder` and none kept in the host policy file, the proposal names no folder and its problems ask for one. The facts include what the file kept from an earlier connect: the paths read outside the folders (`read_paths`) and the scheduled triggers you remembered that are still within their date (`remembered`) |
| `POST /v1/connect` | `{"policy": {...}, "consent": "sha256:..."}` as the proposal answered them: connects this computer as `connect` does after its typed yes, and starts serving. A consent that does not match the policy and the facts the engine shows now, the host policy file as it is then included, is refused `device_not_confirmed`. The answer is the status, with `warnings` when the computer is connected and its name was refused |
| `PUT /v1/credentials` | `{"cella": {"token", "expires_at"}, "platform": {"token", "expires_at"}}`, either or both, of one person, under `--credentials client`. A token for another audience, one that expires within a minute, or tokens of two people are refused `invalid_credentials`; a token in an organization's context `credentials_org_context`; once the device's owner is confirmed, another person's token `credentials_other_person` |
| `GET /v1/policy`, `PUT /v1/policy` | The host policy file; a change of `name`, `folders` or `network`, checked as `connect` checks it, with a new name set among your devices. A change of the folders or the network is read again by the serving engine, which restarts its connection and stops the commands running; a new name alone leaves them running |
| `DELETE /v1/policy/remembered/{trigger}` | Forget a remembered trigger; `latere devices policy --forget` |
| `POST /v1/remove` | Remove this device, as `latere devices remove` does: every command stops, the key and the host policy file are deleted, the activity log stays. `{"local": true}` forgets it here alone, for a device already removed or another account's |
| `GET /v1/activity?follow=1` | The activity log as newline-delimited JSON, and with `follow=1` each line as it is written. `since=` takes a duration such as `1h` or a time |
| `GET /v1/approvals?follow=1` | The sessions waiting for an answer, and with `follow=1` every one asked, answered or withdrawn, and every session that starts on the computer, asked or not (`"kind": "started"`, with the agent, the session, who started it and the folder), which is what an application notifies of. While a client holds this stream, a session that asks waits for an answer |
| `POST /v1/approvals/{id}` | `{"answer": "allow" \| "deny" \| "remember", "client": "Your app"}`. `remember` only for a trigger that fires on a schedule. The first answer wins, and every client sees it |
| `POST /v1/pause`, `POST /v1/resume` | Disconnect, stopping every command on the computer, and connect again; `latere devices pause` and `resume` |
| `POST /v1/reload` | Read the host policy file again and connect anew |

Every refusal is `{"error": {"code", "message", "details": {"detail"}}}`.
A step that needs your sign-in from the application and found no token
in time is refused `device_credentials_needed`.

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
| `device_sandbox_unavailable` | On Linux, install `srt` and what it needs; the detail names it. On macOS, the detail names what Seatbelt lacks. |
| `device_policy_invalid` | Fix the host policy file; `latere devices policy` shows what is wrong. |
| `device_removed` | The computer was removed from your devices. Run `latere devices connect` to connect it again. |
| `device_worker_limit` | Another computer is serving with this device's key right now. If that is not you, remove the device. |
| `device_worker_replaced` | Another process connected with this device's key and took over. If that was not you, remove the device. |
| `device_sign_in_expired` | Your sign-in and the key have both expired. Run `latere login`, then connect again. |
| `device_not_serving` | `latere devices pause` or `resume` found nothing serving this computer. Start it with `latere devices connect`. |
| `device_not_confirmed` | Nothing was connected: type yes at the screen, or ask the proposal again and connect with its consent. |
| `device_rename_failed` | The new name was not set among your devices, so nothing changed. Try again. |
| `device_credentials_needed` | The application serving this computer has not handed its tokens; it does on its own, or sign in to it again. |
