---
title: "`latere cella` on the Cella core at the platform origin"
status: implemented
depends_on:
  - specs/001-auth-unification-migration.md
  - specs/007-models-at-the-origin.md
  - "CROSS-REPO: cella docs/client.md, the exported client latere.ai/x/cella/client, v0.6.3"
  - "CROSS-REPO: auth deploy/base/clients.yaml, the latere-cli row's actor_audiences"
affects:
  - internal/commands/cella.go (rebuilt on latere.ai/x/cella/client; the retired subcommands leave)
  - internal/commands/pty.go (the shell attaches through the core's attach socket)
  - internal/commands/exit.go (a remote exit code without the retired lifecycle states)
  - internal/api/client.go (no Cella default, no SANDBOX_API_URL, no Cella error text)
  - internal/api/actor.go (the product URL the issuer is inferred from)
  - cmd/latere/*_e2e_test.go (the cella set runs against a fake core under /v1/environments)
  - .lateregate.yaml (the identity block's audience)
  - docs/cella.md, docs/configuration.md, docs/login-and-tokens.md, README.md, CHANGELOG.md
created: 2026-09-26
updated: 2026-09-26
author: changkun
---

# `latere cella` on the Cella core at the platform origin

The hosted sandbox service at `cella.latere.ai` is retired. Its successor is
the open source Cella control plane, served under the platform origin at
`https://api.latere.ai/v1/environments`, which exports a Go client,
`latere.ai/x/cella/client`. The `latere cella` commands move onto that client:
same command group, the core's grammar, and nothing of the retired API left.

## Design

### Address and credential

| Setting | Value |
|---|---|
| Base URL | `https://api.latere.ai/v1/environments`; `LATERE_CELLA_URL` or `--api-url` overrides it. The URL includes the base path, which the client maps each `/v1/...` route onto |
| Bearer | an actor token minted at auth for the audience `cella`, cellad's own audience, re-minted before it lapses; `LATERE_CELLA_TOKEN` presents a bearer as given |
| Issuer | inferred from the base URL's host (`api.latere.ai` gives `auth.latere.ai`), or `AUTH_URL` |
| HTTP client | the CLI's instrumented client with no `Timeout` |

The client asks for the bearer once per request. A command that runs longer
than one five minute token, such as an attach or a large import, gets a fresh
token on its next request without restarting.

The HTTP client is the CLI's own, never the exported client's default
transport. That transport allows ten seconds to the first response byte, and
a create held until the sandbox runs, like a synchronous command, answers only
when it is done.

`SANDBOX_API_URL` is not read. It named the retired host, and a value left in
a shell profile would point every command at it.

### Commands

| Command | Core route | Behavior |
|---|---|---|
| `apply -f FILE [--wait[=D]]` | `PUT /sandboxes/{name}` when the manifest names one, else `POST /sandboxes` | Prints the sandbox. Without `--wait` it is usually `Pending`, and a line on stderr says how to wait. With `--wait` (ten minutes, or `D`) the server holds the answer until the sandbox runs or fails; a `Failed` sandbox exits 1 with its reason |
| `list [--json]` | `GET /sandboxes`, every page | one record per sandbox: name, id, phase, reason, image, resources, created |
| `get REF` | `GET /sandboxes/{ref}` | the object as the core answered it |
| `start REF`, `stop REF` | `POST /sandboxes/{ref}/start`, `/stop` | prints the sandbox |
| `delete REF` | `DELETE /sandboxes/{ref}` | |
| `exec REF [--env K=V] [--cwd D] [--timeout D] -- CMD` | `POST /sandboxes/{ref}/exec?wait=1` | writes the command's stdout and stderr to the CLI's own, notes a cut output, exits with the command's code |
| `shell REF [-- CMD]`, alias `attach` | `GET /sandboxes/{ref}/attach` | a terminal; the window follows the local one; exits with the shell's code |
| `run --ephemeral --rm [--image I] [--cpu Q] [--memory Q] [--disk N] [--env K=V] [--cwd D] [--timeout S] [--json] -- CMD` | create held with `?wait=1`, then exec, then delete | the create-then-use flow; the sandbox is deleted on every path, a failed start included |
| `logs REF [-f] [--tail N] [--since T]` | `GET /sandboxes/{ref}/logs` | the sandbox's main process output |
| `export REF [PATH...] [--src-dir D] [-o F]` | `GET /sandboxes/{ref}/files` | a tar of the paths; relative paths resolve under `--src-dir`, default `/workspace` |
| `import REF [--input F] [--dest D]` | `PUT /sandboxes/{ref}/files?dest=` | tar, compressed tar, zip or one regular file, converted to tar on the way |
| `upload REF SRC... [--dest D]` | `PUT /sandboxes/{ref}/files?dest=` | files and folders as one tar, folder structure kept |
| `cat`, `write`, `ls`, `mkdir`, `rm`, `mv` | the one-file routes | one file operation each; relative paths resolve under `/workspace` |

The manifest is sent as written, JSON or YAML, and the core decodes it
strictly, so a manifest names `apiVersion: cella.latere.ai/v1beta1`. Applying
a manifest that names a sandbox twice updates it rather than creating a
second, which is what `--idempotency-key` bought on the retired API.

A sandbox's phase is shown as the core reports it (`Pending`, `Queued`,
`Running`, `Stopped`, `Failed`, `Lost`, `Recovering`, `Deleting`), with its
reason where it has one.

### Removed, with the reason

| Removed | Reason |
|---|---|
| `policy`, `policy list` | the core has no named policy profiles; a sandbox's boundary is its manifest |
| `rename` | a sandbox's name is fixed at creation |
| `extend`, `convert` | the core has no tiers or deadlines to move; `spec.lifecycle` sets `autoStop`, `ttl` and `autoDelete` at creation |
| `resize` | a sandbox's resources are fixed at creation |
| `run REF -- CMD` (a background command and its id), `run --follow`, `run --detach`, `run status`, `run logs`, `run cancel`, `logs REF CMD_ID`, `wait REF CMD_ID` | the core runs a command to completion and keeps no command records; `exec` runs a command in an existing sandbox |
| `--credential` | the trust-plane credential catalog is retired; a core Secret is mounted by the manifest's `spec.secrets` |
| `--idempotency-key` | an apply by name is idempotent |
| `shell --session` | the core's attach socket has no session ids |
| the sandbox's tier and deadline in the output | the core has neither |

`logs REF` changes meaning: it was a background command's output and is the
sandbox's main process output.

### Egress

The core enforces a sandbox's egress boundary, and the hosted plane holds an
organization's sandboxes to an allowlist. The CLI sets no boundary of its own:
`apply` sends the manifest's, and `run --ephemeral` sends none, taking the
plane's default for the caller. The docs say so, with an example boundary.

## Acceptance criteria

| # | Criterion | How it is checked |
|---|---|---|
| 1 | Every kept command reaches its core route under a `/v1/environments` base, with the bearer | `internal/commands` tests and the built binary against a fake core |
| 2 | `apply --wait` and `run --ephemeral --rm` hold the create with `?wait=1`; a `Failed` sandbox exits 1 with its reason, and `run` deletes it | fake core |
| 3 | `run --ephemeral --rm` deletes the sandbox after the command, and on a failed command or a failed start | fake core |
| 4 | `shell` drives the attach socket: input up, output down, exit code | fake core with a WebSocket |
| 5 | Every request goes through the CLI's HTTP client | a counting transport |
| 6 | The default base URL is `https://api.latere.ai/v1/environments`, overridden by `LATERE_CELLA_URL` and `--api-url`; the issuer is inferred from it | `internal/commands` and `internal/api` tests |
| 7 | The bearer is an actor token for `cella`, and never the login token | the product audience test |
| 8 | The removed commands are unknown, and no source names `cella.latere.ai` or `SANDBOX_API_URL` | a test over the command tree; a grep test |
