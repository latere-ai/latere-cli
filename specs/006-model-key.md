---
title: "A model key on first use: the CLI's credential for the Lux core's model endpoints"
status: implemented
depends_on:
  - specs/005-lux-env-redesign.md
  - "CROSS-REPO: auth specs/084-keys-by-context.md, member keys, the key events, POST /me/keys"
  - "CROSS-REPO: platform specs/92-keys-by-context.md, the CLI half it names"
  - "CROSS-REPO: latere-ai/specs decisions/2026-09-23-keys-by-context.md"
affects:
  - internal/modelkey/ (new: the key's record, the keychain and file stores, first-use creation)
  - internal/api/keys.go (new: POST and DELETE /me/keys at auth)
  - internal/commands/lux.go (env, token and invoke present the key to the core; `lux key`)
  - internal/commands/auth.go (logout revokes and forgets the keys of the login)
  - docs/lux.md
  - go.mod (github.com/zalando/go-keyring)
created: 2026-09-24
updated: 2026-09-24
author: changkun
---

# A model key on first use

## Why

After the Lux cutover the model endpoints are the Lux core's, at
`https://api.latere.ai/v1/models/<door>`, and the core matches the bearer a
model SDK sends by the SHA-256 of a key it was told about. It does not take
the actor token the CLI mints for the hosted plane today
(`internal/commands/lux.go:616-632`). A person's CLI therefore needs a key:
created in their current context, kept on the machine, and presented as the
bearer of every model call.

The maintainer decided on 2026-09-23 that the CLI creates that key on first
use and keeps it in the system keychain (keys by context). On 2026-09-24
the platform decided how the key reaches the core: the CLI creates it at
auth, and auth's `key.created` event carries the value's SHA-256 to
platformd, which registers it at the core (auth spec 084).

## Current state

| Fact | Where |
|---|---|
| The login is a 0600 JSON file, `~/.config/latere/auth-token.json`; nothing uses the system keychain | `internal/api/token.go:35-83` |
| `lux env`, `lux token` and `lux invoke` present an actor token for the hosted plane, minted from the login | `internal/commands/lux.go:616-632`, `internal/api/actor.go:84-97` |
| The current context is the login token's `org_id`; `latere org` switches it with the refresh-token grant | `internal/commands/auth.go:143-180`, `:640-660` |
| The Lux base URL defaults to `https://lux.latere.ai`, `LUX_API_URL` or `--lux-url` overrides it | `internal/commands/lux.go:290-320` |

## Design

### Which endpoints take the key

The key is presented when the resolved Lux base URL is the Lux core behind
the origin: its path ends in `/v1/models`, the capability prefix the origin
routes to the core (latere-ai/specs decision 2026-09-20). The hosted plane,
`lux.latere.ai`, keeps the actor token until it is deleted in ps-09 step 5.
Flipping the default base URL and porting `lux models`, `usage`, `access`
and `serve` to the core is the CLI's cutover change, not this spec.

### Creating the key

On the first `lux env`, `lux token` or `lux invoke` against the core with no
usable key for the current login and context, the CLI calls auth with the
saved login:

```
POST {auth}/me/keys
Authorization: Bearer <login access token>
{"name": "latere-cli on <hostname>",
 "grants": [{"type": "latere-authz", "actions": ["lux:model.use"],
             "datatypes": ["Model"], "locations": ["https://api.latere.ai"]}],
 "expires_at": "<now + 90 days>",
 "org_id": "<the login's org_id, omitted in the personal context>"}
```

The grant is `model.use` on every Model, with no identifier: which Models
the key reaches is decided by the person's model list (personal) or the
organization's, and money by the context's wallet (platform specs 92, 93).

| auth answers | The CLI |
|---|---|
| `201`, `status: active` | stores the key and uses it |
| `201`, `status: pending_approval` | stores it and says an org admin must approve it; a model call is refused until then |
| `403 member_keys_off` | says the organization does not let members create keys, and to ask an org admin |
| `403 not_a_member`, `409 key_limit`, anything else | the error, with auth's message |

Decision D1, the expiry: 90 days, renewed silently when a call finds it
within 7 days of the end. A key that never expires lives on every laptop the
person ever used; one that is renewed costs a request per quarter.

### Where the key is kept

| | Store | For | Against |
|---|---|---|---|
| A | the system keychain (macOS Keychain, the Secret Service on Linux, the Windows Credential Manager) through `github.com/zalando/go-keyring`, which uses no cgo | the value is encrypted at rest and never in a file a backup or a sync tool copies | a headless Linux host, a container or a CI runner has no Secret Service |
| B | a 0600 file beside the login, `~/.config/latere/model-keys.json` | works everywhere the login does | the value is as exposed as the login, which is a refresh token of the whole account |

**Decision D2: A, falling back to B** when the keychain answers that it is
unavailable. The maintainer asked for the keychain; the fallback is what
keeps the CLI working in the sandboxes and runners it also runs in, and it
exposes nothing the login file does not. `LATERE_MODEL_KEY` in the
environment overrides both, for a CI job that is handed a key.

A record is keyed by the auth base URL, the login's `sub` and the context
(the org id, or `personal`), so switching context or account never presents
another context's key. It holds the key's id, prefix, value, context, status,
creation and expiry. The keychain entry is service `latere-cli model key`,
account `<auth base>|<sub>|<context>`, secret the JSON record.

### The first call

platformd registers the key from auth's event, which a loop delivers every
few seconds, so a key used the moment it is created is refused
`unauthenticated` for a short while. `lux invoke` retries a `401` for up to
30 seconds, with a growing wait, when the key is under two minutes old. A
`401` for an older key means it was revoked or lost: the CLI revokes it at
auth, forgets it, creates a new one, and retries once more under the same
rule. `lux env` and `lux token` make no call; when they print a key created
in that invocation they say it becomes usable within a minute.

### `latere lux key`, and logout

```
latere lux key            the current context's key: prefix, context, status, expiry, store
latere lux key revoke     revoke it at auth and forget it
```

`latere logout` revokes and forgets every key of the login it ends, best
effort: a revocation auth does not answer is reported and the local copy is
forgotten anyway.

## Acceptance criteria

| # | Criterion | Test |
|---|---|---|
| 1 | Against a core base URL with no stored key, `lux token` creates a key at auth with the `model.use` grant, the 90-day expiry, the host name and the login's org, stores it, and prints its value | `internal/commands/lux_key_test.go`, a stub auth |
| 2 | The personal context sends no `org_id`; a switch of context uses a different record | same |
| 3 | `pending_approval`, `member_keys_off` and `key_limit` each answer their sentence | same |
| 4 | Against the hosted plane the actor token is presented as before | the existing `lux env` tests |
| 5 | The keychain is used when available; an unavailable keychain falls back to the file with 0600; `LATERE_MODEL_KEY` wins over both | `internal/modelkey/store_test.go`, the mock keyring |
| 6 | `lux invoke` retries a fresh key's `401` until the core accepts it, and replaces an old key that is refused | `lux_key_test.go`, a stub core |
| 7 | A key within 7 days of its expiry is replaced before use | `internal/modelkey` |
| 8 | `lux key revoke` and `logout` revoke at auth and forget the record | `lux_key_test.go`, `auth_test.go` |

## Not in this spec

The CLI's cutover change: the default base URL and the other `lux`
commands against the core. The console's keys (platform spec 92).

## Outcome

Built on 2026-09-24, not released: the release waits for the platform's
word, since the key reaches the core only once auth's key events and
platformd's sink are live. Every criterion has a test in
`internal/commands/lux_key_test.go` or `internal/modelkey/modelkey_test.go`;
criterion 4's hosted half is the existing `lux env` tests. The command tests
run with an in-memory keychain, so no test touches the developer's.

Where the build differs from the design above: a keychain index entry,
`latere-cli model key slots`, lists the slots the keychain holds, because a
keychain cannot be enumerated portably and logout needs every slot of the
login.

