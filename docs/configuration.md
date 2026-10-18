# Configuration

Everything `latere` reads from the environment, and every file it keeps.
A command-line flag wins over the environment variable for the same
setting, and each command's `--help` names the flags it takes.

## Environment variables

### Signing in

| Variable | Default | What it does |
|----------|---------|--------------|
| `AUTH_URL` | `https://auth.latere.ai` | The auth service `login`, `logout`, `whoami`, and `org` talk to, and the one product commands mint their tokens at. A product command with its own URL override and no `AUTH_URL` derives the auth URL from the product URL. `--auth-url` overrides it. |
| `AUTH_CLIENT_ID` | `latere-cli` | The OAuth client for refresh, organization switching, and logout when the saved login names none. A login made with `--client-id` keeps that client. |
| `LATERE_AUTH_TOKEN_FILE` | `~/.config/latere/auth-token.json` | Where the login token is kept. |
| `XDG_CONFIG_HOME` | `~/.config` | The base of the CLI's configuration directory, `$XDG_CONFIG_HOME/latere`. |

### Products

| Variable | Default | What it does |
|----------|---------|--------------|
| `LATERE_ENVIRONMENTS_URL` | `https://api.latere.ai/v1/environments` | The Environments API base URL, including its `/v1/environments` path. `--api-url` overrides it. |
| `LATERE_ENVIRONMENTS_TOKEN` | none | A bearer to present to the Environments API instead of minting one. |
| `LATERE_MODELS_URL` | `https://api.latere.ai/v1/models` | The model endpoints of the Latere API, which `latere models`, `latere agents review` and `latere agents run` call. `--models-url` overrides it. |
| `LATERE_MODEL_KEY` | none | A model key to present to the Latere API instead of the one the CLI keeps. With it, the model commands need no login. |
| `LATERE_MODEL_KEYS_FILE` | `~/.config/latere/model-keys.json` | Where model keys are kept on a machine with no system keychain. |
| `LATERE_AGENT_PROVIDER_FILE` | `agent-provider.json` in your user configuration directory | The model provider `latere agents provider` saved for `latere agents run`. |
| `LATERE_CLAUDE_TOKEN_FILE` | `claude.json` in your user configuration directory | The Claude sign-in `latere agents provider` saved. |
| `ANTHROPIC_API_KEY` | none | An Anthropic API key. When set, `latere agents run` calls Anthropic directly with it, ahead of every other provider. |
| `CLAUDE_CODE_OAUTH_TOKEN` | none | A Claude Code token `latere agents run` falls back to when you have no saved provider and no `latere` login. |
| `LATERE_APPS_URL` | `https://api.latere.ai/v1/apps` | The Apps API base URL, including its `/v1/apps` path, which `latere apps` calls. `--api-url` overrides it. |
| `LATERE_APPS_TOKEN` | none | A bearer to present to the Apps API instead of minting one. |
| `LATERE_PLATFORM_URL` | `https://platform.latere.ai` | The platform `latere repos` creates, lists and reads repositories on, `latere apps visibility`, `password`, `access`, `share` and `unshare` reach the people invited to an app and its password on, and `latere devices` lists, names and removes your devices on. `--platform-url` overrides it. |
| `LATERE_PLATFORM_TOKEN` | none | A bearer to present to the platform instead of minting one. |
| `CODE_HOST` | `code.latere.ai` | The Latere Code host the git credential helper answers for, and the one `latere repos` prints clone URLs for. A nonblank value also allows plain HTTP, for a development host. |
| `EVAL_API_URL` | `https://eval.latere.ai` | The Eval API. `--api-url` overrides it. |
| `LATERE_DEVICE_KEYS_FILE` | unset | A file to keep this computer's device key in instead of the keychain, for a computer whose keychain needs a person to unlock it. |
| `EVAL_ADMIN_TOKEN` | none | The bearer every `latere eval` command presents: a token the issuer minted for the `eval` audience, for a platform administrator or a service account. `--token` overrides it. |

Your user configuration directory is the operating system's: `~/.config`
on Linux (or `$XDG_CONFIG_HOME`), and `~/Library/Application Support` on
macOS.

### Upgrades and logs

| Variable | Default | What it does |
|----------|---------|--------------|
| `LATERE_NO_UPDATE_CHECK` | unset | Any value turns off the daily release check and auto-upgrade. |
| `CI` | unset | Any value turns them off as well, so a build never upgrades itself. |
| `XDG_STATE_HOME` | `~/.local/state` | The base of the directory `latere agents review` writes its logs to, `$XDG_STATE_HOME/latere/reviews`, and of `latere devices`' state, `$XDG_STATE_HOME/latere/devices`. |
| `XDG_CACHE_HOME` | `~/Library/Caches` on macOS, `~/.cache` on Linux | The base of the directory a connected computer keeps its sandboxes' scratch directories in, `$XDG_CACHE_HOME/latere/devices`. |

Every upgrade, automatic or with `latere upgrade`, checks the release's
signature against the Latere keys built into `latere` before it trusts any
checksum, and refuses a release that is not signed by Latere, or one older
than v0.21.0, the first signed release. No variable or flag turns the check
off. When auto-upgrade refuses a release, it says so once and tries that
release again a day later.

## Files

In `$XDG_CONFIG_HOME/latere`, or `~/.config/latere`:

| File | What it holds |
|------|---------------|
| `auth-token.json` | The login token and its refresh token, readable by you alone. `latere logout` deletes it. |
| `model-keys.json` | Model keys for the Latere API, only on a machine with no system keychain. Elsewhere they are in the keychain under `latere-cli model key`. |
| `device.yaml` | The host policy of a computer connected with `latere devices connect`: the folders sessions may use, the network, read paths and remembered triggers ([devices.md](devices.md#the-host-policy-file)). Readable by you alone. |
| `device-keys.json` | The connected computer's key, only on a machine with no keychain or with `LATERE_DEVICE_KEYS_FILE` naming another file. Elsewhere it is in the keychain under `latere-cli device key`. |
| `config.json` | Your auto-upgrade choice. |
| `update-check.json` | When the CLI last checked for a release, what it found, and the last release auto-upgrade refused for its signature. |

In your user configuration directory, under `latere/`:

| File | What it holds |
|------|---------------|
| `agent-provider.json` | The model provider `latere agents run` uses, and an Anthropic API key when you chose one. A choice saved by an earlier version under another name is moved here the first time it is read. |
| `claude.json` | The Claude sign-in `latere agents provider` saved. |

`latere agents review` writes each run's logs to
`$XDG_STATE_HOME/latere/reviews/<repo-key>/` and prunes old ones; see
[agents.md](agents.md#review-log-location).

A connected computer keeps, in `$XDG_STATE_HOME/latere/devices/`, the lock
that keeps one `connect` or `serve` per computer, the control socket
`control.sock`, and each device's activity log, `<device>/activity.ndjson`,
rotated at 10 MB with five older files kept. Its sandboxes' scratch
directories are under `$XDG_CACHE_HOME/latere/devices/<device>/`. No
session can read the configuration or the state directory
([devices.md](devices.md#what-a-session-can-reach)).
