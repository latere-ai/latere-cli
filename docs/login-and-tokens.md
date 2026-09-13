# CLI login and tokens

You sign in to Latere once, and every `latere` command reaches its
product with your identity. This page explains what `latere login` puts
on disk, how each product gets the credential it needs, how the CLI
keeps you signed in, and what `latere logout` tears down.

The shape to keep in mind: one human login to auth produces a **login
token** addressed to auth alone; every product call asks auth for a
short-lived token addressed to that one product. Your identity (`sub`
and `org_id`) is the same on every hop; the token on the wire changes at
each product boundary, because each product accepts only tokens that
name it.

## Signing in

```sh
latere login
```

This runs the OAuth 2.0 device authorization flow (RFC 8628) against
`auth.latere.ai`. The CLI prints a verification URL and a user code,
opens your browser, and waits while you approve. On approval, auth
returns your login token and the CLI writes one file under
`~/.config/latere/`.

```sh
latere login --org-id <org-uuid>   # sign in scoped to an organization
latere login --personal            # sign in in your personal context
latere login --no-browser          # print the URL instead of opening a browser
```

Use `latere org <org-uuid>` or `latere org --personal` to switch context
without another browser approval. Switching replaces the login token, and
every product token is minted from it, so the next command already follows
the new context. Choose either an organization ID or `--personal`;
combining them is rejected without changing credentials, including through
`latere auth org switch`.

For a pre-issued token, use `latere login --token <token>` or pipe the
token to `latere login`. A pasted token keeps its existing context: omit
`--personal` and `--org-id`, which apply only to browser login. The CLI
asks auth to confirm the token before saving it, because auth is who the
token is addressed to. A rejected token, an unreachable auth, or a
cancelled attempt leaves your saved login intact. A pasted token carries
no refresh grant, so nothing of a previous login survives beside it.

Device login reports success once the approved token is saved. If saving
fails, the CLI reports an error and your previous login is untouched. Fix
the reported storage problem and log in again.

For a custom OAuth client, pass `latere login --client-id <client-id>`.
The CLI retains that client ID for refresh, organization switching, and
logout, even if `AUTH_CLIENT_ID` later changes. Older token files without
a saved client ID use `AUTH_CLIENT_ID`, falling back to `latere-cli`.

## The one token file

| File | What it is | Used for |
|------|------------|----------|
| `~/.config/latere/auth-token.json` | The **login token**: an `auth.latere.ai`-issued access token plus its refresh token. | The one credential on disk. It is presented to auth alone: to refresh itself and to mint the product tokens below. |

Nothing else is stored. A product token lives for one command in memory
and lapses within five minutes, so writing it beside the login would put
a second, longer-lived credential on your filesystem for nothing.

The file is replaced atomically with `0600` permissions on each save
(best-effort on Windows), including when an existing file has broader
access. Concurrent readers see a complete token file. A symlink at the
token path is replaced; its target is left unchanged.

## How each product gets its credential

Every product call asks auth for a token addressed to that product. One
function in the shared library makes the request, so every product
follows the same rule.

| Command | What is sent | Where it comes from |
|---|---|---|
| `latere cella ...` | a token with audience `sandboxd`, 5 minutes | minted per command at auth |
| `latere drive ...` | a token with audience `drive.latere.ai`, 5 minutes | minted per command at auth |
| `latere lux invoke`, `models`, `usage`, `access` | a token with audience `lux.latere.ai`, 5 minutes | minted per command at auth |
| `latere lux env`, `lux token` | a token with audience `lux.latere.ai`, 5 minutes | minted when the command runs; re-run for a new one |
| `latere lux serve`, `latere review`, the local model route | a token with audience `lux.latere.ai`, re-minted a minute before it expires | minted for the session, so a tunnel that runs for hours never sends the login token |
| `git` against `code.latere.ai` | a token with audience `origo`, 5 minutes | minted per git operation at auth |
| `latere topos ...` | a token with audience `toposd`, 5 minutes | minted per command at auth |
| `latere whoami` | the login token | read from `auth-token.json`; it goes to auth, which is who it names |

An actor token comes from auth's `POST /actor-tokens`: you present the
login token, name one audience, and get back a short-lived token carrying
your own `sub` and `org_id` and nothing else. It is valid at that one
product and worthless anywhere else. Auth mints only for the audiences
this client is registered to act at, and the token lives at most five
minutes — the CLI has no shorter-lifetime option, because a mint costs
one round trip and a command that outlives its token mints again before
its next request. A streaming response — `latere cella logs --follow`, a
file export — holds the token it opened with; a stream that outlives it
ends and the command reports it.

Your login token is addressed to `auth.latere.ai` and opens nothing else.
A product refuses it, which is the point: a token that names the issuer
is a credential to your account, not to one product.

### Cella

`latere cella` presents a token with audience `sandboxd`, minted when the
command builds its client. A command that outlives it re-mints before the
next request, and a `401` from Cella mints once more and retries that
request. A streaming response is the exception: it holds the token it
opened with.

Set `LATERE_CELLA_TOKEN` to present a bearer of your own instead, for a
development deployment or a test. `LATERE_DRIVE_TOKEN`, `LATERE_LUX_TOKEN`
and `TOPOS_TOKEN` do the same for their products.

### Lux

For CLI-initiated model calls (`latere lux invoke`, `models`, `usage`,
`access`), the CLI mints a token with audience `lux.latere.ai` and
presents it to Lux for that one call. The call finishes in seconds, so
the short lifetime bounds a leaked value at no cost to you.

`lux env` and `lux token` export a credential for a stock SDK. The value
they export is always a Lux-bound token, never your login token, and
stderr reports when it expires:

```sh
eval "$(latere lux env --compat openai)"   # a Lux-bound token, five minutes
```

`lux env` needs a surface: either `--compat <dialect>` or a passthrough
provider argument. See the "Models (Lux)" page for the full surface.

### Git: Latere Code

`latere login` also wires a git credential helper scoped to Latere Code
(`code.latere.ai`) in your global git config, so
`git clone https://code.latere.ai/<owner>/<repo>.git` authenticates with
no token in the URL. When git asks the helper for a credential on that
host, the CLI refreshes your login if it has expired, mints a token for
Origo's audience (`origo`), and hands git that token. Origo accepts only
tokens carrying its audience, so your login token never reaches git and
the token is useless anywhere else.

If the login cannot be read or refreshed, or auth cannot mint the token,
the git helper returns no credential so git can prompt. Nothing else is
ever substituted for the token it could not mint.

Without a saved login there is nothing to mint from. Drive and the git
helper then refuse with `not logged in; run `latere login``, and nothing
is sent. Run `latere login` to sign in again.

```sh
latere git-credential setup             # wire the helper manually
latere login --no-git                   # sign in without touching git config
```

The helper answers only for that host over HTTPS. A nonblank `CODE_HOST`
override also permits HTTP for development. Setup registers both HTTPS
and HTTP for an overridden host; `setup --remove` removes both. Missing
or other protocols receive no credential and do not trigger a refresh.
`store` and `erase` are no-ops: your login lives in `~/.config/latere`,
managed by `latere login` and `latere logout`, never in git's own
credential store.

## Staying signed in

The CLI refreshes the login near expiry so a long session keeps working
without re-login. If the saved login has expired and has no refresh
token, run `latere login` again. The CLI does not export or send that
expired credential.

When the login token is within a minute of expiry, the CLI refreshes it
against auth using the stored refresh token, requesting the exact same
scope set it requested at login (so a refresh can never silently drop a
scope). The refreshed token and its new refresh token are written back to
`auth-token.json`. If saving fails, refresh reports an error. Restore
access to the credential file and run `latere login` again; auth may
already have invalidated the old refresh token.

Product tokens are not refreshed, only re-minted. There is nothing to
persist: a lapsed one is replaced by a new mint on the next call.

Because every product credential is minted from the login token, keeping
it refreshed keeps every product reachable. The one exception is a
paste-mode login (`latere login --token <token>`): that supplies an access
token with no refresh token, so the CLI cannot refresh it. When it
expires, run `latere login` again.

## Signing out

```sh
latere logout
```

Logout revokes your session on the server, not just the local file:

1. It revokes the refresh token at auth (`POST /revoke`, RFC 7009), so
   the login can mint no further tokens.
2. It deletes `auth-token.json`.

Tokens already minted for a product are not recalled; each lapses within
five minutes.

Server-side revocation is best-effort. If auth cannot revoke (for example
it is unreachable), the CLI prints a note, still clears your local file,
and the refresh token expires on its own. A local removal failure makes
the command exit with an error identifying the path. Fix its permissions
or storage and run `latere logout` again to finish signing out.

## The rule

The CLI holds one rule, which is the platform's:

> Your identity (`org_id`, `sub`) is the same at every product; a
> product's own token never crosses into another product; a
> cross-product hop carries an auth-issued token minted for the far
> product's audience.

In CLI terms: you log in once to auth, and every product call mints its
credential from that login. A `lux.latere.ai` token is only ever
presented to Lux, a `drive.latere.ai` one only to Drive, a `sandboxd` one
only to Cella, an `origo` one only to git. Whichever token is on the
wire, the person it acts for is you.

Leaf id-01 of `specs/infrastructure/identity` made the table above true
for every row on 2026-09-12, and leaf id-03 left one minting function and
one credential on disk; a test over a recording transport holds it for
every product command.

## Scripting surfaces

Two commands hand a raw token to your own scripts. They are deliberate
escape hatches, outside the managed refresh flow above:

```sh
latere print-token                # print the login token (opens auth alone)
latere login --token <token>      # save a pasted access token (no refresh)
```

To hand a credential to a product, ask for that product's own, e.g.
`latere lux env --raw`.

## Configuration

| Setting | Purpose |
|---------|---------|
| `--auth-url` / `AUTH_URL` | Override the auth base URL (default `https://auth.latere.ai`). |
| `--api-url` / `SANDBOX_API_URL` | Override the Cella API base URL. The auth URL is derived from it when no auth override is set. |
| `LATERE_AUTH_TOKEN_FILE` | Override the login token path. |
| `LATERE_CELLA_TOKEN` | Present this bearer to Cella instead of minting one. |
| `CODE_HOST` | Override the Latere Code host the git credential helper answers for. |

Explicit URL flags take precedence over environment variables. The
session commands (`login`, `logout`, `whoami`, `org`) speak to auth and
take `--auth-url`; product commands derive the auth URL from their own
product URL when no override is set.

## Related reading

- Cella: **"Authentication"** and **"Sandbox identity, egress, and agent
  grants"** cover the audience Cella requires and what a token grants
  inside a sandbox.
- Drive: **"Authentication"** covers the audience Drive requires and what
  an actor token is authorized to reach.
- This repo: `latere lux` details in "Models (Lux)", and git access in
  the [main README](../README.md#git-with-latere-code). Start any of
  these with `latere login` (see [Sign in](../README.md#sign-in)).
