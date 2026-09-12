# CLI login and tokens

You sign in to Latere once, and every `latere` command reaches its
product with your identity. This page explains what `latere login`
puts on disk, how each product gets the credential it needs, how the
CLI keeps you signed in, and what `latere logout` tears down.

The shape to keep in mind: one human login to auth mints a **root
token**; each product call derives the credential that product
accepts from that root. Your identity (`org_id` and `sub`) is the same
on every hop; the token presented changes at each product boundary,
because each product accepts only tokens that name it.

## Signing in

```sh
latere login
```

This runs the OAuth 2.0 device authorization flow (RFC 8628) against
`auth.latere.ai`. The CLI prints a verification URL and a user code,
opens your browser, and waits while you approve. On approval, auth
returns your root token and the CLI writes two files under
`~/.config/latere/`.

```sh
latere login --org-id <org-uuid>   # sign in scoped to an organization
latere login --personal         # sign in in your personal context
latere login --no-browser       # print the URL instead of opening a browser
```

Use `latere org <org-uuid>` or `latere org --personal` to switch context
without another browser approval. Switching updates the auth root token and
replaces the Cella bearer so subsequent Cella commands use the selected context.
Choose either an organization ID or `--personal`; combining them is rejected
without changing credentials, including through `latere auth org switch`.
If the Cella exchange fails, the command reports an error and removes the old
Cella bearer; the new auth root token is retained. Retry the switch or log in
again before using Cella.

For a pre-issued Cella token, use `latere login --token <token>` or pipe the
token to `latere login`. A pasted token keeps its existing context: omit
`--personal` and `--org-id`, which apply only to browser login. The CLI rejects
these combinations without changing saved credentials.
The CLI verifies the token before replacing the saved Cella credential.
A rejected token, verification failure, or cancelled
attempt leaves your saved credentials intact. After a successful pasted-token
login, the CLI removes the previous auth root token to avoid using a different
identity for other products. If that removal fails, login reports an error and
removes the newly saved Cella token. Fix the reported storage problem before
retrying login.

Device login reports success after saving both credentials. If saving the auth
root fails after the Cella token was saved, the CLI removes that Cella token and
reports an error. Fix the reported storage problem and log in again. This keeps
a new Cella account from being refreshed using a previous account's auth root.
If removal also fails, the error reports it; repair storage and run
`latere logout` before retrying login.

For a custom OAuth client, pass `latere login --client-id <client-id>`. The CLI
retains that client ID for refresh, organization switching, and logout, even
if `AUTH_CLIENT_ID` later changes. Older token files without a saved client ID
use `AUTH_CLIENT_ID`, falling back to `latere-cli`.

## The two token files

| File | What it is | Used for |
|------|------------|----------|
| `~/.config/latere/auth-token.json` | The **auth root token**: an `auth.latere.ai`-issued access token plus its refresh token. | The source every per-product credential is derived from. It is presented to auth alone: to refresh itself and to mint the actor tokens below. |
| `~/.config/latere/token.json` | The **Cella bearer**: a Cella-issued catalog token, labeled `CLI on <hostname>`. | `latere cella ...` and `latere whoami`. |

The split is deliberate. The two tokens have different issuers
(`auth.latere.ai` for the root, Cella for the bearer) and different
lifetimes, so they live in separate files. The root token can mint
new per-product credentials long after any single product bearer has
expired.

Both files are replaced atomically with `0600` permissions on each save
(best-effort on Windows), including when an existing file has broader access.
Concurrent readers see a complete token file. A symlink at either token file
path is replaced; its target is left unchanged.

## How each product gets its credential

Every product call starts from the auth root token and derives what
that product accepts. The derivation differs by product because the
products validate differently.

| Command | What is sent | Where it comes from |
|---|---|---|
| `latere cella ...`, `latere whoami` | the Cella bearer | `token.json`, minted at login through the chain below |
| `latere drive ...` | an actor token, audience `drive.latere.ai`, 5 minutes | minted per command at auth |
| `latere lux invoke`, `models`, `usage`, `access` | an actor token, audience `lux.latere.ai`, 5 minutes | minted per command at auth |
| `latere lux env`, `lux token` | an actor token, audience `lux.latere.ai`, 5 minutes, or the shorter `--ttl` you name | minted at auth when the command runs; re-run for a new one |
| `latere lux serve`, `latere review`, the local model route | an actor token, audience `lux.latere.ai`, re-minted a minute before it expires | minted at auth for the session, so a tunnel that runs for hours never sends the root token |
| `git` against `code.latere.ai` | an actor token, audience `origo`, 5 minutes | minted per git operation at auth |
| `latere topos ...` | an actor token, audience `toposd`, 5 minutes | minted per command at auth |

An actor token is auth's `POST /actor-tokens`: you present the root
token, name one audience, and get back a short-lived token carrying
your own `sub` and `org_id` and nothing else. It is valid at that one
product and worthless anywhere else.

Your login token itself is registered for three audiences: the auth
issuer, `sandboxd` and `toposd`. It is never sent to a product, because
a token that names the auth issuer is a credential to your account and
not to one product. `lux env` therefore exports an actor token and
never the login token; the export lives at most five minutes and the
command says so on stderr.

### Cella

`latere cella` presents the Cella bearer from `token.json`. That
bearer is minted through a two-step chain at login and whenever it
needs refreshing:

1. Mint a short-lived actor token at auth (`POST /actor-tokens`, audience
   `sandboxd`, 60-second TTL), presenting the root token.
2. Exchange that actor token at Cella (`POST /v1/tokens/exchange`) for a
   catalog bearer labeled `CLI on <hostname>`.

Cella replaces any previous row with the same label, so repeated
exchanges rotate the bearer rather than piling up catalog entries.
Cella is the one product that answers with a credential it signed
itself; every other product takes an auth-issued token directly.

### Lux

For CLI-initiated model calls (`latere lux invoke`, `models`, `usage`,
`access`), the CLI mints a fresh actor token at auth with audience
`lux.latere.ai` and a 5-minute TTL, then presents it to Lux for that
one call. The call finishes in seconds, so the short lifetime bounds a
leaked value at no cost to you.

`lux env` and `lux token` export a credential for a stock SDK. Without
`--ttl` they export your root identity token, which lasts the login
session but is not bound to Lux, and the command says so on stderr.
The hosted Lux requires `aud: lux.latere.ai`, which your login token
does not carry, so pass `--ttl` and get a token Lux accepts:

```sh
eval "$(latere lux env --compat openai --ttl 5m)" # a Lux-bound actor token
eval "$(latere lux env --compat openai)"          # the root identity token, not Lux-scoped
```

`lux env` needs a surface: either `--compat <dialect>` or a passthrough
provider argument. See the "Models (Lux)" page for the full surface.

### Git: Latere Code

`latere login` also wires a git credential helper scoped to Latere Code
(`code.latere.ai`) in your global git config, so
`git clone https://code.latere.ai/<owner>/<repo>.git` authenticates with
no token in the URL. When git asks the helper for a credential on that
host, the CLI refreshes your login if it has expired, mints an actor token
at auth for Origo's audience (`origo`) with a 5-minute TTL, and hands git
that token. Origo accepts only tokens carrying its audience, so your root
identity token never reaches git and the token is useless anywhere else.
A git exchange completes in seconds, so the short lifetime bounds a leaked
value at no cost to you.

If the login cannot be read or refreshed, or auth cannot mint the token, the
git helper returns no credential so git can prompt. An existing auth failure
never causes it to substitute the saved Cella token.

After a `--token` paste login there is no `auth-token.json`, so there
is no root token to mint from. Drive and the git helper then refuse
with `not signed in; run `latere login``, and nothing is sent: the
saved Cella bearer names Cella and is never presented to another
product. Run `latere login` to get a root token back.

```sh
latere git-credential setup             # wire the helper manually
latere login --no-git                   # sign in without touching git config
```

The helper answers only for that host over HTTPS. A nonblank `CODE_HOST`
override also permits HTTP for development. Setup registers both HTTPS and
HTTP for an overridden host; `setup --remove` removes both. Missing or other protocols receive no credential and do not
trigger token refresh.
`store` and `erase` are
no-ops: your tokens live in `~/.config/latere`, managed by `latere
login` and `latere logout`, never in git's own credential store.

## Staying signed in

The CLI refreshes tokens near expiry so a long session keeps working
without re-login. If a saved auth credential has expired and has no refresh
token, run `latere login` again. The CLI does not export or send that expired
credential.

- **Auth root token.** When the root token is within a minute of
  expiry, the CLI refreshes it against auth using the stored refresh
  token, requesting the exact same scope set it requested at login (so
  a refresh can never silently drop a scope). The refreshed root and
  its new refresh token are written back to `auth-token.json`.
  If saving fails, refresh reports an error. Restore access to the credential
  file and run `latere login` again; auth may already have invalidated the old
  refresh token.
- **Cella bearer.** When the bearer in `token.json` is near expiry, or
  a Cella call comes back `401`, the CLI re-runs the mint-and-exchange
  chain from the root token (refreshing the root first if it too is
  near expiry) and writes the replacement bearer back. This happens at
  most once per command, transparently, before your call is retried.

Lux and Topos refresh the root token when needed before making a request.
They do not exchange or replace your Cella credential. If either product
rejects its bearer, the CLI reports that error without retrying with a
Cella token.

Because every product credential derives from the root token, keeping
the root refreshed keeps every product reachable. The one exception is
a paste-mode login (`latere login --token <token>`): that supplies an
access token with no refresh token, so the CLI cannot refresh it. When
it expires, run `latere login` again.

## Signing out

```sh
latere logout
```

Logout revokes your session on the server, not just the local files:

1. It revokes the Cella bearer at Cella (`DELETE /v1/tokens/current`),
   so the catalog token dies immediately instead of lingering until
   its TTL.
2. It revokes the auth refresh token at auth (`POST /revoke`, RFC 7009),
   so the root credential can mint no further tokens.
3. It deletes `token.json` and `auth-token.json`.

Server-side revocation is best-effort. If a server cannot revoke (for
example it is unreachable), the CLI prints a note, still clears your
local files, and the affected token expires on its own. Logout attempts to
remove both local files even if one removal fails. Any local removal failures
make the command exit with an error identifying the affected paths. Fix their
permissions or storage and run `latere logout` again to finish signing out.

## The rule

The CLI holds one rule, which is the platform's:

> Your identity (`org_id`, `sub`) is the same at every product; a
> product's own token never crosses into another product; a
> cross-product hop carries an auth-issued token minted for the far
> product's audience.

In CLI terms: you log in once to auth to obtain the root, and every
product call derives its credential from that root. The Cella bearer
is only ever presented to Cella. A `lux.latere.ai` actor token is only
ever presented to Lux, a `drive.latere.ai` one only to Drive, an
`origo` one only to git. Whichever token is on the wire, the person it
acts for is you.

Leaf id-01 of `specs/infrastructure/identity` made the table above
true for every row on 2026-09-12; a test over a recording transport
holds it for every product command.

## Scripting surfaces

Two commands hand a raw token to your own scripts. They are deliberate
escape hatches, outside the managed refresh flow above:

```sh
latere print-token                # print the Cella bearer from token.json
latere login --token <token>      # save a pasted access token (no refresh)
```

## Configuration

| Setting | Purpose |
|---------|---------|
| `--auth-url` / `AUTH_URL` | Override the auth base URL (default `https://auth.latere.ai`). |
| `--api-url` / `SANDBOX_API_URL` | Override the Cella API base URL, from which the auth URL is derived. |
| `CODE_HOST` | Override the Latere Code host the git credential helper answers for. |

Explicit URL flags take precedence over environment variables. Login uses
the resolved Cella URL for both token exchange and verification. If neither
`--auth-url` nor `AUTH_URL` is set, the auth URL is derived from that Cella URL.
`whoami` also uses `AUTH_URL` when probing an auth-issued token. Lux commands
and the git credential helper use the same auth override precedence
for refreshing the root token and minting product credentials.

## Related reading

- Cella: **"Authentication"** and **"Sandbox identity, egress, and
  agent grants"** cover how the Cella bearer arrives through token
  exchange, what scopes it may carry, and what it grants inside a
  sandbox.
- Drive: **"Authentication"** covers the audience Drive requires and
  what an actor token is authorized to reach.
- This repo: `latere lux` details in "Models (Lux)", and git access
  in the [main README](../README.md#git-with-latere-code). Start any
  of these with `latere login` (see [Sign in](../README.md#sign-in)).
