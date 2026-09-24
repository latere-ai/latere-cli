# Login and tokens

You sign in to Latere once, and every `latere` command reaches its
product with your identity. This page explains what `latere login` puts
on disk, how each product gets the credential it needs, how the CLI keeps
you signed in, and what `latere logout` removes.

The shape to keep in mind: one sign-in at auth produces a **login token**
addressed to auth alone, and every product call asks auth for a
short-lived token addressed to that one product. Your identity, the
subject and the organization, is the same on every call; the token on the
wire changes at each product, because each product accepts only tokens
that name it.

## Signing in

```sh
latere login
```

This runs the OAuth 2.0 device authorization flow (RFC 8628) against
`auth.latere.ai`. The CLI prints a verification URL and a user code, opens
your browser, and waits while you approve and choose your personal account
or an organization. On approval, auth returns your login token and the CLI
saves it.

```sh
latere login --org-id <org-uuid>   # sign in to an organization
latere login --personal            # sign in to your personal account
latere login --no-browser          # print the URL instead of opening a browser
```

`latere org <org-uuid>` and `latere org --personal` switch context without
another browser approval: the CLI exchanges the saved refresh token for a
login token in the new context and replaces the saved one. Every product
token is minted from it, so the next command already follows the new
context. Name either an organization or `--personal`; asking for both is
refused and leaves your credentials unchanged.

For a token issued elsewhere, such as in CI, use
`latere login --token <token>` or pipe the token to `latere login`. A
pasted token keeps its own context, so `--personal` and `--org-id` apply
only to browser sign-in. The CLI asks auth to confirm the token before
saving it. A rejected token, an unreachable auth, or a canceled attempt
leaves your saved login as it was. A pasted token carries no refresh
token, so nothing of a previous login survives beside it.

Device sign-in reports success once the approved token is saved. If saving
fails, the CLI reports an error and your previous login is untouched; fix
the storage problem it names and sign in again.

For a custom OAuth client, pass `latere login --client-id <client-id>`.
The CLI keeps that client for refresh, organization switching, and logout,
even if `AUTH_CLIENT_ID` later changes. A login saved without a client
uses `AUTH_CLIENT_ID`, and then `latere-cli`.

Every sign-in also configures git for Latere Code unless you pass
`--no-git`; see [Git: Latere Code](#git-latere-code) below.

## What is stored

| Where | What it is | Used for |
|-------|------------|----------|
| `~/.config/latere/auth-token.json` | The **login token**: an access token issued by `auth.latere.ai`, and its refresh token. | Presented to auth alone: to refresh itself, and to mint every product token. |
| The system keychain, or `~/.config/latere/model-keys.json` | **Model keys** for the Latere API, one per context, created on first use. | Presented to the model endpoints of the Latere API. See [Lux](lux.md#your-model-key-on-the-latere-api). |

Product tokens are never written to disk. Each lives in memory for one
command and lapses within five minutes, so saving it beside the login
would put a second, longer-lived credential on your file system for
nothing.

The token file is replaced atomically and readable by you alone (`0600`,
best effort on Windows), including when an existing file had broader
permissions. A concurrent reader always sees a complete file. A symlink at
the token path is replaced, and its target is left unchanged.

`latere topos login` keeps its own model provider choice for
`latere topos --local`, which uses no Latere identity; see
[Topos](topos.md#choosing-the-model). [configuration.md](configuration.md)
lists every file the CLI keeps.

## How each product gets its credential

Every product call asks auth for a token addressed to that product.

| Command | What is sent | Where it comes from |
|---|---|---|
| `latere cella ...` | a token with audience `sandboxd`, valid 5 minutes | minted per command at auth |
| `latere lux invoke`, `models`, `usage`, `access` | a token with audience `lux.latere.ai`, valid 5 minutes | minted per command at auth |
| `latere lux env` | a token with audience `lux.latere.ai`, valid 5 minutes | minted when the command runs; run it again for a new one |
| `latere lux serve`, `latere review`, `latere topos --local` through Lux | a token with audience `lux.latere.ai`, minted again a minute before it expires | minted for the session, so a tunnel that runs for hours never sends the login token |
| `latere lux` against the Latere API | a model key | created at auth on first use and kept, as above |
| `git` against `code.latere.ai` | a token with audience `origo`, valid 5 minutes | minted per git operation at auth |
| `latere topos ...` | a token with audience `toposd`, valid 5 minutes | minted per command at auth |
| `latere drive ...` | a token with audience `drive.latere.ai`, valid 5 minutes | minted per command at auth |
| `latere whoami` | the login token, only to refresh it when it is due | the claims it prints are read from the saved token, not asked of auth |

Auth mints a product token from your login token: you name one audience
and get back a token carrying your own subject and organization and
nothing else, valid at that one product and useless anywhere else. Auth
mints only for the audiences this client is registered for, and a token
lives at most five minutes. A command that outlives its token mints again
before its next request. A streaming response, such as
`latere cella logs --follow` or a file export, keeps the token it opened
with, and a stream that outlives it ends with an error.

Your login token is addressed to `auth.latere.ai` and opens nothing else.
A product refuses it, which is the point: a token that names the issuer is
a credential to your account, not to one product.

### Cella

`latere cella` mints a `sandboxd` token when the command builds its
client. A command that outlives it mints again before the next request,
and a `401` from Cella mints once more and retries that request.

Set `LATERE_CELLA_TOKEN` to present a bearer of your own instead, for a
development deployment or a test. `LATERE_LUX_TOKEN`, `TOPOS_TOKEN`, and
`LATERE_DRIVE_TOKEN` do the same for their products.

### Lux

For a model call the CLI makes itself (`latere lux invoke`, `models`,
`usage`, `access`), it mints a `lux.latere.ai` token and presents it for
that one call.

`latere lux env` exports a credential for a stock SDK. The value is always
a token minted for Lux, never your login token, and stderr says when it
expires:

```sh
eval "$(latere lux env --compat openai)"   # a Lux token, valid five minutes
```

`lux env` needs a surface: `--compat <dialect>` or a provider argument.
[lux.md](lux.md) covers both, and the model key the Latere API takes.

### Git: Latere Code

`latere login` configures a git credential helper for Latere Code
(`code.latere.ai`) in your global git config, so
`git clone https://code.latere.ai/<owner>/<repo>.git` authenticates with
no token in the URL. When git asks the helper for a credential on that
host, the CLI refreshes your login if it has expired, mints a token with
Latere Code's audience, `origo`, and hands git that token. Your login
token never reaches git, and the token git holds is useless anywhere else.

If the login cannot be read or refreshed, or auth does not mint the token,
the helper returns no credential and git prompts as it would without a
helper. Nothing else is ever substituted for the token it could not mint.
Without a saved login there is nothing to mint from: the helper, and
`latere drive`, refuse with "not logged in; run `latere login`" and send
nothing.

```sh
latere git-credential setup             # configure the helper yourself
latere git-credential setup --remove    # remove it
latere login --no-git                   # sign in without touching git config
```

The helper answers only for that host, over HTTPS. A nonblank `CODE_HOST`
names another host and also allows plain HTTP, for development; `setup`
then registers both schemes and `setup --remove` removes both. A request
for any other host or protocol gets no credential and triggers no refresh.
`store` and `erase` do nothing: your login lives in `~/.config/latere`,
managed by `latere login` and `latere logout`, never in git's own
credential store.

## Staying signed in

When the login token is within a minute of expiry, the CLI refreshes it at
auth with the saved refresh token, asking for exactly the scopes it asked
for at sign-in, so a refresh never drops one. The new token and its new
refresh token are written back to `auth-token.json`. If saving fails, the
command reports an error: restore access to the file and run
`latere login`, because auth may already have invalidated the old refresh
token.

Product tokens are not refreshed, only minted again; there is nothing to
save. Because every product token is minted from the login token, keeping
the login fresh keeps every product reachable.

A login saved with `latere login --token` has no refresh token, so the CLI
cannot refresh it. When it expires, sign in again. The CLI never sends or
exports an expired login token.

## Signing out

```sh
latere logout
```

Logout revokes your session on the server, not only the local file:

1. It revokes the model keys this login created on this machine.
2. It revokes the refresh token at auth (`POST /revoke`, RFC 7009), so the
   login can mint nothing further.
3. It deletes the local copies of those keys and `auth-token.json`.

Tokens already minted for a product are not recalled; each lapses within
five minutes.

Revocation is best effort. If auth cannot be reached, the CLI prints a
warning, still clears your local files, and the refresh token expires on
its own. If a local file cannot be removed, the command exits with an
error naming the path; fix its permissions and run `latere logout` again.

## The rule

The CLI keeps one rule, which is the platform's:

> Your identity is the same at every product; a product's own token never
> crosses into another product; a call from one product to another
> carries a token auth minted for the far product's audience.

In CLI terms: you sign in once, and every product call mints its
credential from that login. A `lux.latere.ai` token is only ever
presented to Lux, a `sandboxd` token only to Cella, an `origo` token only
to Latere Code, and a `toposd` token only to Topos. Whichever token is on
the wire, the person it acts for is you.

## Scripting

Two commands hand a raw credential to your own scripts, outside the
managed refresh described above:

```sh
latere print-token                # print the login token, which auth alone accepts
latere login --token <token>      # save a token issued elsewhere, with no refresh
```

To hand a credential to a product, ask for that product's own, for example
`latere lux env --raw` for Lux.

## Settings

| Setting | Purpose |
|---------|---------|
| `--auth-url` / `AUTH_URL` | The auth service, `https://auth.latere.ai` by default. |
| `--client-id` / `AUTH_CLIENT_ID` | The OAuth client, `latere-cli` by default. |
| `LATERE_AUTH_TOKEN_FILE` | Where the login token is kept. |
| `CODE_HOST` | The Latere Code host the git credential helper answers for. |

The session commands (`login`, `logout`, `whoami`, `org`) talk to auth and
take `--auth-url`. A product command with its own URL override derives the
auth URL from it when neither `--auth-url` nor `AUTH_URL` is set, so a
development deployment mints at the auth service beside it. Every other
variable is in [configuration.md](configuration.md).

## Related reading

- [Cella authentication](https://platform.latere.ai/docs/cella/authentication):
  the audience Cella requires and what a token grants inside a sandbox.
- [Lux keys](https://platform.latere.ai/docs/lux/keys): the model keys the
  Latere API takes.
- [Latere Code access](https://platform.latere.ai/docs/repos/access): who
  may read and push a repository.
