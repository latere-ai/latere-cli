---
title: "A desktop client of the device engine: `latere devices serve` starts on a computer not yet connected and serves its control socket in every state, connects through the socket with the consent a desktop application showed, takes short-lived Cella and platform tokens from that client in place of the saved login, changes the host policy and removes the device through the socket, and makes no release check when told not to"
status: complete
depends_on:
  - specs/016-devices-command.md
  - specs/017-signed-upgrades.md
  - "CROSS-REPO: latere-ai/gradus specs/014-this-mac.md, Latere Studio's This Mac, which bundles this command and drives `serve` as its client"
  - "CROSS-REPO: latere-ai/auth deploy/base/clients.yaml, the `latere-studio` client whose actor audiences include `cella` and `api.latere.ai`"
  - "CROSS-REPO: latere-ai/platform specs/220-devices.md, what platformd allows at create and `/v1/account/devices`"
affects:
  - internal/devices/control.go (the routes below)
  - internal/devices/connect.go (new: the connect's facts, terms and consent; the engine's connect, policy, credentials and removal)
  - internal/devices/engine.go (an engine that serves its socket before it is connected; credentials from the client; the policy and removal routes)
  - internal/devices/credentials.go (new: the tokens a client hands the engine, and when it needs more)
  - internal/devices/policy.go (the policy checked and applied through the socket)
  - internal/devices/paths.go (`OwnerFile`, whose device this computer serves)
  - internal/devices/owner.go (new: the record of whose device this computer serves)
  - internal/devices/events.go (`Status` says the connection state and the credentials needed)
  - internal/commands/devices_connect.go (enrollment shared by connect and `POST /v1/connect`; `serve --credentials client`; the release check off under `LATERE_NO_UPDATE_CHECK`; Latere Studio's folders denied; whether another process serves the computer)
  - internal/commands/devices.go (`remove` shares its local half with `POST /v1/remove`)
  - internal/commands/devices_records.go (`deviceAuth`: the saved login or the client's tokens, for every call a device makes as the person; `logout` keeps a device another process serves)
  - internal/upgrade/notify.go (`Disabled`, which `serve` asks before it checks for releases)
  - internal/commands/auth.go (`logout` on a computer a desktop application serves)
  - docs/devices.md, CHANGELOG.md
effort: medium
created: 2026-10-19
updated: 2026-10-21
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
- The engine's `Account` and the enrollment take the person's tokens from the saved login, `~/.config/latere/auth-token.json`, refreshed as the `latere-cli` client (`cellaClient`, `platformCall`). A desktop app has its own sign-in and must not share its refresh token: a refresh token is spent once (auth spec 090), so two holders of one strand each other. The static escapes, `LATERE_ENVIRONMENTS_TOKEN` and `LATERE_PLATFORM_TOKEN`, expire under a serve that runs for weeks.
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

`GET /v1/connect?name=&folder=&network=&host=&session_start=` answers
the proposal for those values, filled as `connect` fills them (the
computer's name, the default hosts, what an earlier file said) and checked
as `connect` checks them, with the facts the connect screen states. A
folder is absolute or under `~`, with `:ro` for read-only; `folder` and
`host` repeat. Each fact is a sentence on one line, what it names, and a
sentence after it, so the client wraps and lays them out:

```json
{
  "policy": {"name": "MacBook Pro", "session_start": "allow", "folders": [{"path": "~/code/site", "write": true}], "network": {"mode": "allowlist", "hosts": ["github.com", "..."]}},
  "facts": [
    {"kind": "folders", "text": "Sessions you start in Latere will run commands and read and edit files on this computer, in these folders only:", "items": ["~/code/site (read-write)"]},
    {"kind": "secrets", "text": "Everything in these folders is readable to the agent, a project's .env file and other secrets kept there included."},
    {"kind": "network", "text": "Network: commands may reach these hosts only:", "items": ["github.com", "..."], "note": "A host that accepts writes, such as a git host, can carry data out."},
    {"kind": "who", "text": "Only sessions you start, and triggers you own, can use this computer. Sessions you start, and your triggers that run on a schedule, start on this computer without asking here. ..."},
    {"kind": "log", "text": "What the agent reads on this computer goes to the session's log and to the model. In an organization's conversation, that organization can read it too."}
  ],
  "consent": "sha256:<hex of the policy and the facts as JSON>",
  "problems": []
}
```

The `who` fact follows `session_start`, with the same sentences the
screen prints for allow and for ask. What the host policy file kept from
an earlier connect is granted too, so it is a fact as well: `read_paths`,
the paths read outside the folders, and `remembered`, the scheduled
triggers that start without asking and are still within their date,
each only when the file holds one.
The terminal's screen states them in the same words, and reads as before
for a file that holds neither. With no `folder` asked and none kept in
the file, the proposal names no folder, and its problems ask for one:
`serve` runs where its application started it, often `/`, which the
terminal's default, the folder it runs in, would propose. The proposal
on a computer already connected is refused `device_already_connected`.

`POST /v1/connect` with `{"policy": {...}, "consent": "<digest>"}` does
what `connect` does after its typed yes, through the same function: it
refuses a digest that does not match the policy and facts the engine
would show now, the file as it is then included, so a file changed since
the proposal fails it (`device_not_confirmed`); the posted name is
cleaned as the proposal's was. It writes `device.yaml`, creates
the environment at Cella and names it at the platform with the client's
credentials (B), and starts serving. Its refusals are 016's
(`device_policy_invalid`, `device_personal_only`, `device_limit`,
`device_org_context`, `sign_in_required`), and
`device_credentials_needed` when no client handed a token in time, as
the JSON error. A name the platform refuses leaves the computer
connected: the answer is the status with
`"warnings": ["This computer is connected, and its name ... was not set: ..."]`.
The terminal's `ConnectScreen` renders the same facts, so the two never
drift.

### B. Credentials from the client

`serve --credentials client` never reads the saved login. The engine
holds what a client gave it:

`PUT /v1/credentials` with
`{"cella": {"token": "...", "expires_at": "..."}, "platform": {"token": "...", "expires_at": "..."}}`,
each an actor token minted by the client for that audience from its own
sign-in in the person's personal context. The engine checks each token's
audience and expiry, that it names a person, and that it is in that
person's own context (`credentials_org_context` otherwise: a device is
personal); tokens handed together must name one person. It keeps them in
memory only and uses them for `MintKey`, `RevokeKey`, `Exists` and the
enrollment's platform calls. A token is never written to a file or the
activity log.

The tokens are tied to the person whose device the computer serves,
once the platform has confirmed who that is: a token of another person
is then refused (`credentials_other_person`, 409), and tokens held for
another are dropped. Without the tie, an application signed in as
someone else would have Cella answer `not_found` or `device_not_owned`,
which the engine reads as a removal, and the computer would forget a
device still its owner's at the platform. The owner is recorded in the
state directory, `owner.json` beside the control socket, apart from
`device.yaml` so that every version still reads that file, and only as
the platform confirmed it: at a connect (through the socket, the person
whose tokens connected it; from the terminal, the saved login's person),
and, for a device connected before the record existed, the first time
the platform mints the computer's key for a person or answers their
sign-in as the device's own (under the saved login, `serve` asks once
at start). Until then, any person's tokens are taken, and when the
platform answers them `not_found` or `device_not_owned`, the device is
kept: the tokens are dropped, and the status reads offline, asking for
the owner's sign-in. On a computer not connected, any person's tokens
are taken, and another person's replace them, so the next connect may be
anyone's.

`GET /v1/status` gains `"credentials": {"cella": "valid" | "needed",
"platform": "valid" | "needed"}`, and `GET /v1/status?follow=1` streams
the status, one JSON object a line, each time it changes, including when
a token turns `needed`: none held, or the held one within a minute of
its expiry. A follower that falls behind is dropped, its stream ending,
and reconnects. A key rotation or an existence check that needs a token
waits up to 30 seconds for one; without one it keeps serving on the
current key and tries again at the next check, as an expired saved login
does today. A key that must be minted (none kept, or the kept one
expired) waits for a token however long it takes, offline with the
status saying the token is needed, and the engine and its socket stay
up meanwhile; the next steps it names are the application's, never
`latere login`. A stop while it waits ends `serve` cleanly. Every other step that needs a token and found none in
time is refused `device_credentials_needed`. Without
`--credentials client`, the saved login is used as today.

### C. The host policy through the socket

`GET /v1/policy` answers the policy as the engine reads it.
`PUT /v1/policy` with any of `name`, `folders`, `network` checks the
result as `connect` does (`Problems`, `CheckDriver`), refuses with
`device_policy_invalid` and the problem, or writes `device.yaml` under
the engine's policy lock, answering the status. A change of the folders
or the network is then read again by the serving engine, which restarts
its worker and so stops every running session; a new name alone, or no
change, does not reload and leaves them running. A changed name is set
at the platform first (`PATCH /v1/account/devices/{id}`), with the
client's credentials and outside the policy lock, so a session start is
never held up by the platform; the file is then read again and the
change applied over it. A refusal there is `device_rename_failed`, and
nothing is written.
`DELETE /v1/policy/remembered/{trigger}` forgets one remembered
scheduled trigger, and answers `not_remembered` for one it does not
name.

### D. Removing through the socket

`POST /v1/remove` does what `latere devices remove` does for this
computer, with the client's credentials: `DELETE
/v1/account/devices/{id}`, then the key and `device.yaml` deleted, the
activity log kept, every sandbox stopped, and the engine back in
`not_connected`. With `{"local": true}` it deletes only the local half,
for a computer whose device belongs to another account or is already
removed at the platform. `latere devices remove` and the route share one
function. A removal while the computer is paused deletes its sandboxes
and their directory too, and a removal that comes between a connect and
the start of serving finds the start checking the file under the same
lock, so no device the file no longer names is served. The file goes
even when the worker saw the removal at the platform first and cleared
its device. The record of the owner goes with the device.

### E. No release check when told not to

`serve` passes no `Upgrade` to the engine when `LATERE_NO_UPDATE_CHECK`
is set or `CI` is, as every other release awareness already honors
(`disabled`, `internal/upgrade/notify.go`).

### F. Latere Studio's folders denied to every session

On macOS the engine `latere devices` runs denies every session Latere
Studio's data and cache, `~/Library/Application Support/ai.latere.studio`
and `~/Library/Caches/ai.latere.studio`, beside the engine's own paths.
The app's own engine denies them too (gradus spec 014), so a session never
reads the app's local copy of conversations, whichever process serves the
computer. Elsewhere there is no such app and nothing more is denied.

The engine's `Config` has no deny list of its own: it builds the local
driver's options from the host policy and `Paths.Deny`, and hands them to
the embedder's `NewDriver`. The command's `sessionDriver` adds the two
paths there, and the policy checks of `connect` and `latere devices policy`
build the same driver, so a read path inside the app's data is refused
before a session starts. The first design, a `deny` list in the host
policy written through `PUT /v1/policy`, is not built: the folders are the
app's and fixed, so neither the person nor a client needs to write them.

### G. Logout on a computer another process serves

`latere logout` on a connected computer takes the engine's lock and lets
it go at once. When another process holds it (`device_already_served`),
such as Latere Studio or `latere devices` in another terminal, the device
is that process's too: logout signs out, keeps the device, asks nothing,
also under `--yes`, and says so in one sentence: "Latere Studio or
`latere devices` in another terminal serves this computer, so it stays
connected as your device "Work laptop" (dev-…); to remove it, run
`latere devices remove` while you are signed in." With the lock free,
`logout` asks and removes as 016 says. A lock that cannot be taken for
another reason stays signed in with `device_logout_blocked`.

The first design read the engine's status for `--credentials client`.
Latere Studio embeds the engine rather than running `serve`, so the lock,
which every embedder takes, is what says another process serves. No flag
removes the device anyway: `latere devices remove` does, as logout's
blocked refusal already says, and `--yes` keeps meaning only the answer to
logout's question.

### Out of scope

- Running `serve` as a launchd agent or systemd unit.
- A client on another user account or another computer: the socket stays the person's own, by peer credentials.
- A second engine per computer.

## Verification

| # | Test | Where |
|---|---|---|
| 1 | `TestServeWaitsNotConnected` and `TestServeWaitsNotConnectedCommand`: `serve` with no device answers `not_connected` on the socket and creates nothing | `internal/devices/socket_test.go`, `internal/commands/devices_socket_test.go` |
| 2 | `TestConnectThroughSocket` and `TestConnectThroughSocketWithClientCredentials`: `POST /v1/connect` with the consent from `GET /v1/connect` creates the environment `connect` creates and names it, and a stale consent is refused `device_not_confirmed` | `internal/devices/socket_test.go`, `internal/commands/devices_socket_test.go` |
| 3 | `TestConnectScreenMatchesFacts`: every fact the route answers is on the terminal's screen | `internal/devices/socket_test.go` |
| 4 | `TestClientCredentials` and `TestConnectThroughSocketWithClientCredentials`: tokens are checked and kept in memory, a call waits for one, a held one turns `needed` a minute before its expiry; under `--credentials client` with a saved login no process may read, every Cella and platform request presents the handed token, and no token reaches a file, the log or the output | `internal/devices/socket_test.go`, `internal/commands/devices_socket_test.go` |
| 5 | `TestPolicyThroughSocket`: a bad folder is refused with its problem; a refused rename writes nothing; a good change reloads and the driver serves the new grants; a remembered trigger is forgotten | `internal/devices/socket_test.go` |
| 6 | `TestRemoveThroughSocket` and its `local` form, with `serve` waiting again afterwards | `internal/devices/socket_test.go`, `internal/commands/devices_socket_test.go` |
| 7 | `TestServeHonorsNoUpdateCheck`: no release lookup under `LATERE_NO_UPDATE_CHECK` or `CI` | `internal/commands/devices_account_test.go` |
| 8 | `TestServedSessionsAreDeniedStudiosFolders`: on macOS every driver the engine builds denies Latere Studio's data and cache beside the engine's own paths, and `latere devices policy` refuses a read path in the app's data; `TestSessionDenyIsMacOSOnly`: nothing more is denied elsewhere | `internal/commands/devices_deny_darwin_test.go`, `internal/commands/devices_test.go` |
| 9 | `TestLogoutKeepsAnApplicationsDevice`: with the lock held by a child process, logout with and without `--yes` revokes the login, keeps the key and the device, asks nothing and says so; with the lock free it removes the device and leaves the lock free | `internal/commands/logout_served_test.go` |
| 10 | `TestDevicesServeThroughTheSocketEndToEnd`: the built binary under `--credentials client` with no saved login, against a real cellad, from tokens through connect, rename and remove | `cmd/latere/devices_e2e_test.go` |
| 11 | `TestRenameLeavesRunningSessionsAlone` and `TestRenameRunsOutsideTheFileLock`: a rename, an empty change and the same folders restart no worker, a change of the folders does, and a session start reads the file during a slow rename | `internal/devices/client_engine_test.go` |
| 12 | `TestTokensAreTiedToTheDevicesOwner`, `TestAnUnconfirmedPersonNeverForgetsTheDevice`, `TestSavedLoginIsRecordedAsTheOwner` and `TestConnectCreatesTheEnvironment`: an organization's token and two people's are refused; with a recorded owner another person's is refused and the device is not forgotten; without one, another person's tokens are dropped and the device kept, and the owner is recorded once the platform mints for them; the saved login and a terminal connect record the owner; after a removal anyone's are taken | `internal/devices/client_engine_test.go`, `internal/commands/devices_test.go` |
| 13 | `TestClientModeWaitsForTheKey` and `TestStopDuringTheRenewalWaitIsClean`: with no key and no token, `serve` waits offline with its socket up, and serves once tokens are handed; a stop during a renewal's wait is clean | `internal/devices/client_engine_test.go` |
| 14 | `TestConsentCoversWhatTheFileKept` and `TestConnectScreenUnchangedWithoutKeptGrants`: the facts name the file's read paths and remembered triggers, a file changed after the proposal fails the consent, and the screen is byte-identical for a file that keeps neither | `internal/devices/client_engine_test.go` |
| 15 | `TestStatusCanBeFollowed`: the stream says a token is needed, valid after it is handed, and needed again before it expires | `internal/devices/client_engine_test.go` |
| 16 | `TestConnectAnswersARefusedNameAndCleansIt`, `TestAMissingTokenIsSaidSo`, `TestAProposalWithNoFolderAsksForOne`, `TestRemovalWhilePausedDeletesTheSandboxes`, `TestRemovalClearsTheKeyToRevoke`, `TestAStartAfterARemovalServesNothing`, `TestRemovalDeletesAFileTheWorkerAlreadyCleared`, `TestRemovalKeepsAnotherDevicesOwnerRecord` | `internal/devices/client_engine_test.go` |

## Progress

A to E are built (2026-10-19), with tests 1 to 7 and 10, and released in
v0.25.0. An independent review of that release found five things to
change for a desktop client, built since with tests 11 to 16: a rename
no longer restarts the worker, handed tokens are tied to the device's
owner and an organization's are refused, a key to mint waits for the
client's token instead of ending `serve`, the consent covers the read
paths and remembered triggers the file kept, and the status can be
followed; with them, the rename runs outside the policy lock, a missing
token is answered `device_credentials_needed`, a proposal with no folder
asks for one, and a removal while paused or right after a connect leaves
nothing served or kept. A second review then found that the owner tie did
not hold for a device connected before the record existed, and that a
stop during a key renewal's wait ended with a refusal; both are fixed:
the owner is recorded only as the platform confirms it, an unconfirmed
person's removal answer never forgets the device, and the stop is
clean. F and G are built (2026-10-21) with tests 8 and 9, on Cella
v0.20.1.

Since 2026-10-21 the engine these routes belong to is Cella's
`latere.ai/x/cella/device` (Cella v0.20.0): the `internal/devices` files
in `affects` moved there with their tests, and `serve` embeds it with the
platform's part of a connect over the platform's API.

## Outcome

A to G are built, each with its verification items. A to E were released
in v0.25.0; F and G are built on 2026-10-21, not released. Two parts
diverged from the first design, as their sections say: F denies Latere
Studio's fixed folders in the command's driver builder instead of a `deny`
list in the host policy, and G asks the engine's lock whether another
process serves the computer instead of the engine's status, since the app
embeds the engine rather than running `serve`.
