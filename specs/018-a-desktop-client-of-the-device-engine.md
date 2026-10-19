---
title: "A desktop client of the device engine: `latere devices serve` starts on a computer not yet connected and serves its control socket in every state, connects through the socket with the consent a desktop application showed, takes short-lived Cella and platform tokens from that client in place of the saved login, changes the host policy and removes the device through the socket, and makes no release check when told not to"
status: in-progress
depends_on:
  - specs/016-devices-command.md
  - specs/017-signed-upgrades.md
  - "CROSS-REPO: latere-ai/gradus specs/014-this-mac.md, Latere Studio's This Mac, which bundles this command and drives `serve` as its client"
  - "CROSS-REPO: latere-ai/auth deploy/base/clients.yaml, the `latere-studio` client whose actor audiences include `cella` and `api.latere.ai`"
  - "CROSS-REPO: latere-ai/platform specs/220-devices.md, what platformd allows at create and `/v1/account/devices`"
affects:
  - internal/devices/control.go (the routes below)
  - internal/devices/engine.go (an engine that serves its socket before it is connected; credentials from the client; the policy and removal routes)
  - internal/devices/credentials.go (new: the tokens a client hands the engine, and when it needs more)
  - internal/devices/policy.go (the policy checked and applied through the socket; extra denied paths)
  - internal/devices/paths.go (`Deny` adds the policy's denied paths)
  - internal/devices/events.go (`Status` says the connection state and the credentials needed)
  - internal/commands/devices_connect.go (enrolment shared by connect and `POST /v1/connect`; `serve --credentials client`; the release check off under `LATERE_NO_UPDATE_CHECK`)
  - internal/commands/devices.go (`remove` shares its local half with `POST /v1/remove`)
  - internal/commands/auth.go (`logout` on a computer a desktop application serves)
  - docs/devices.md, CHANGELOG.md
effort: medium
created: 2026-10-19
updated: 2026-10-19
author: changkun
---

# A desktop client of the device engine

## Why

Spec 016 makes everything a computer does to serve sessions one engine,
and gives a desktop application `latere devices serve` and a control
socket to drive it: "No other program reimplements the engine or the
worker protocol: they are clients of this one." Latere Studio, Latere's
Mac app, is the first such client (gradus spec 014). It bundles this
command and runs `serve` as its child, or attaches to an engine a
terminal already runs.

As built, a desktop client cannot get a computer from nothing to
serving, keep it serving, or change it, without a terminal:

- `serve` refuses a computer not yet connected (`device_not_connected`), and connecting is only `latere devices connect`, which waits for a typed yes in a terminal (`enrollDevice`, `internal/commands/devices_connect.go`).
- The engine's `Account` and the enrolment take the person's tokens from the saved login, `~/.config/latere/auth-token.json`, refreshed as the `latere-cli` client (`cellaClient`, `platformCall`). A desktop app has its own sign-in and must not share its refresh token: a refresh token is spent once (auth spec 090), so two holders of one strand each other. The static escapes, `LATERE_ENVIRONMENTS_TOKEN` and `LATERE_PLATFORM_TOKEN`, expire under a serve that runs for weeks.
- A change of folders or network is an edit of `device.yaml` and `POST /v1/reload`, so a client would write the file's format itself, and a check the engine makes at connect (`Policy.Problems`, `CheckDriver`) would not be made.
- Removing the device is `latere devices remove`, which reads the saved login.
- `serve` asks GitHub for the newest release once a day (`upgradeAvailable`) even under `LATERE_NO_UPDATE_CHECK`, which a bundled copy never acts on.
- The engine denies its own files and the keychains to every session, and a client has no way to add its own data.
- `latere logout` on a connected computer removes the device, which a desktop application on the same computer also serves.

What already works for a bundled copy, and stays: the release keys are
in the binary, so `deviceStart`'s verified-build check passes; the
auto-upgrade runs only with a terminal on standard error, so a child of
an app never replaces itself; the key store goes through
`/usr/bin/security`, so the terminal's command and the bundled one read
one keychain item; the lock and the socket let a second client attach.

## Design

Each part is a route on the control socket of spec 016, versioned under
`/v1`, in the family's JSON and error envelope, or a small change to a
command. None adds a second way to do what the engine does.

### A. Serving before connecting, and connecting through the socket

`serve` on a computer whose `device.yaml` names no device no longer
refuses. It takes the lock, opens the control socket, and waits in a new
state, `not_connected`, which `GET /v1/status` answers with no device.
The terminal's `connect` is unchanged.

`GET /v1/connect?name=&folder=&network=&host=` answers the proposal for
those values, filled as `connect` fills them (the computer's name, the
default hosts) and checked as `connect` checks them, with the facts the
connect screen states, each as a field and in its words:

```json
{
  "policy": {"name": "MacBook Pro", "folders": [{"path": "~/code/site", "write": true}], "network": {"mode": "allowlist", "hosts": ["github.com", "..."]}},
  "facts": [
    {"kind": "folders", "text": "Sessions you start in Latere will run commands and read and edit files on this computer, in these folders only."},
    {"kind": "secrets", "text": "Everything in these folders is readable to the agent, a project's .env file and other secrets kept there included."},
    {"kind": "network", "text": "A host that accepts writes, such as a git host, can carry data out."},
    {"kind": "who", "text": "Only sessions you start, and triggers you own, can ask to use this computer, and each session needs your approval here before it starts."},
    {"kind": "log", "text": "What the agent reads on this computer goes to the session's log and to the model. In an organization's conversation, that organization can read it too."}
  ],
  "consent": "<a digest of the policy and the facts>",
  "problems": []
}
```

`POST /v1/connect` with `{"policy": {...}, "consent": "<digest>"}` does
what `connect` does after its typed yes, through the same function: it
refuses a digest that does not match the policy and facts the engine
would show now (`device_not_confirmed`), writes `device.yaml`, creates
the environment at Cella and names it at the platform with the client's
credentials (B), and starts serving. Its refusals are 016's
(`device_policy_invalid`, `device_personal_only`, `device_limit`,
`device_org_context`, `sign_in_required`), as the JSON error. The
terminal's `ConnectScreen` renders the same facts, so the two never
drift.

### B. Credentials from the client

`serve --credentials client` never reads the saved login. The engine
holds what a client gave it:

`PUT /v1/credentials` with
`{"cella": {"token": "...", "expires_at": "..."}, "platform": {"token": "...", "expires_at": "..."}}`,
each an actor token minted by the client for that audience from its own
sign-in in the person's personal context. The engine checks each token's
audience and expiry, keeps them in memory only, and uses them for
`MintKey`, `RevokeKey`, `Exists` and the enrolment's platform calls. A
token is never written to a file or the activity log.

`GET /v1/status` gains `"credentials": {"cella": "valid" | "needed",
"platform": "valid" | "needed"}`, and a status event is sent when one
turns `needed`: none held, or the held one within a minute of its
expiry. A key rotation or an existence check that needs a token waits
up to 30 seconds for one; without one it keeps serving on the current
key and tries again at the next check, as an expired saved login does
today. Without `--credentials client`, the saved login is used as
today.

### C. The host policy through the socket

`GET /v1/policy` answers the policy as the engine reads it.
`PUT /v1/policy` with any of `name`, `folders`, `network` checks the
result as `connect` does (`Problems`, `CheckDriver`), refuses with
`device_policy_invalid` and the problem, or writes `device.yaml` under
the engine's policy lock and reloads, answering the status. A changed
name is also set at the platform (`PATCH /v1/account/devices/{id}`) with
the client's credentials. `DELETE /v1/policy/remembered/{trigger}`
forgets one remembered scheduled trigger.

### D. Removing through the socket

`POST /v1/remove` does what `latere devices remove` does for this
computer, with the client's credentials: `DELETE
/v1/account/devices/{id}`, then the key and `device.yaml` deleted, the
activity log kept, every sandbox stopped, and the engine back in
`not_connected`. With `{"local": true}` it deletes only the local half,
for a computer whose device belongs to another account or is already
removed at the platform. `latere devices remove` and the route share one
function.

### E. No release check when told not to

`serve` passes no `Upgrade` to the engine when `LATERE_NO_UPDATE_CHECK`
is set or `CI` is, as every other release awareness already honors
(`disabled`, `internal/upgrade/notify.go`).

### F. Paths a client denies

The host policy gains `deny`, a list of paths (absolute or `~`) added to
`Paths.Deny` for every session, written through `PUT /v1/policy` like
the rest. A desktop application adds its own data directory.

### G. Logout on a computer an application serves

`latere logout` on a connected computer whose engine runs under
`--credentials client` (its status says so) signs out of the command
alone and leaves the device, saying "This computer stays connected for
Latere Studio." Removing it is `latere devices remove`. Without such an
engine, `logout` asks as 016 says.

### Out of scope

- Running `serve` as a launchd agent or systemd unit.
- A client on another user account or another computer: the socket stays the person's own, by peer credentials.
- A second engine per computer.

## Verification

| # | Test | Where |
|---|---|---|
| 1 | `TestServeWaitsNotConnected`: `serve` with no device answers `not_connected` on the socket and creates nothing | `internal/devices/control_test.go` |
| 2 | `TestConnectThroughSocket`: against a stub Cella and platform, `POST /v1/connect` with the digest from `GET /v1/connect` creates the same environment `connect` creates, and a stale digest is refused `device_not_confirmed` | `internal/commands/devices_test.go` |
| 3 | `TestConnectScreenMatchesFacts`: the terminal's screen and the route's facts are one source | `internal/devices/terminal_test.go` |
| 4 | `TestClientCredentials`: under `--credentials client`, with no `auth-token.json`, a key is minted with the handed token, status turns `needed` before expiry, and no token reaches a file or the log | `internal/devices/engine_test.go` |
| 5 | `TestPolicyThroughSocket`: a bad folder is refused with its problem; a good change reloads and the driver serves the new grants | `internal/devices/policy_test.go` |
| 6 | `TestRemoveThroughSocket` and its `local` form | `internal/commands/devices_test.go` |
| 7 | `TestServeHonorsNoUpdateCheck`: no release lookup under `LATERE_NO_UPDATE_CHECK` | `internal/commands/devices_test.go` |
| 8 | `TestDenyFromPolicy`: a denied path is unreadable to a stage, skipped where `srt` is missing | `internal/devices/engine_test.go` |
| 9 | `TestLogoutKeepsAnApplicationsDevice` | `internal/commands/logout_device_test.go` |
| 10 | The end-to-end run of 016, driven through the socket alone from connect to remove | `cmd/latere/devices_e2e_test.go` |
