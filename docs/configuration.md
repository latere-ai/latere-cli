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
| `LATERE_CELLA_URL` | `https://api.latere.ai/v1/environments` | The Cella API base URL, including its `/v1/environments` path. `--api-url` overrides it. |
| `LATERE_CELLA_TOKEN` | none | A bearer to present to Cella instead of minting one. |
| `LATERE_MODELS_URL` | `https://api.latere.ai/v1/models` | The model endpoints of the Latere API, which `latere models`, `latere review` and `latere topos --local` call. `--models-url` overrides it. |
| `LATERE_MODEL_KEY` | none | A model key to present to the Latere API instead of the one the CLI keeps. With it, the model commands need no login. |
| `LATERE_MODEL_KEYS_FILE` | `~/.config/latere/model-keys.json` | Where model keys are kept on a machine with no system keychain. |
| `LATERE_TOPOS_PROVIDER_FILE` | `topos-provider.json` in your user configuration directory | The model provider `latere topos login` saved for `latere topos --local`. |
| `LATERE_CLAUDE_TOKEN_FILE` | `claude.json` in your user configuration directory | The Claude sign-in `latere topos login` saved. |
| `ANTHROPIC_API_KEY` | none | An Anthropic API key. When set, `latere topos --local` calls Anthropic directly with it, ahead of every other provider. |
| `CLAUDE_CODE_OAUTH_TOKEN` | none | A Claude Code token `latere topos --local` falls back to when you have no saved provider and no `latere` login. |
| `LATERE_APP_URL` | `https://api.latere.ai/v1/apps` | The Apps API base URL, including its `/v1/apps` path, which `latere app` calls. `--api-url` overrides it. |
| `LATERE_APP_TOKEN` | none | A bearer to present to the Apps API instead of minting one. |
| `LATERE_PLATFORM_URL` | `https://platform.latere.ai` | The platform `latere repos` creates, lists and reads repositories on. `--platform-url` overrides it. |
| `LATERE_PLATFORM_TOKEN` | none | A bearer to present to the platform instead of minting one. |
| `CODE_HOST` | `code.latere.ai` | The Latere Code host the git credential helper answers for, and the one `latere repos` prints clone URLs for. A nonblank value also allows plain HTTP, for a development host. |
| `EVAL_API_URL` | `https://eval.latere.ai` | The Eval API. `--api-url` overrides it. |
| `EVAL_ADMIN_TOKEN` | none | The bearer every `latere eval` command presents: a token the issuer minted for the `eval` audience, for a platform administrator or a service account. `--token` overrides it. |

Your user configuration directory is the operating system's: `~/.config`
on Linux (or `$XDG_CONFIG_HOME`), and `~/Library/Application Support` on
macOS.

### Upgrades and logs

| Variable | Default | What it does |
|----------|---------|--------------|
| `LATERE_NO_UPDATE_CHECK` | unset | Any value turns off the daily release check and auto-upgrade. |
| `CI` | unset | Any value turns them off as well, so a build never upgrades itself. |
| `XDG_STATE_HOME` | `~/.local/state` | The base of the directory `latere review` writes its logs to, `$XDG_STATE_HOME/latere/reviews`. |

## Files

In `$XDG_CONFIG_HOME/latere`, or `~/.config/latere`:

| File | What it holds |
|------|---------------|
| `auth-token.json` | The login token and its refresh token, readable by you alone. `latere logout` deletes it. |
| `model-keys.json` | Model keys for the Latere API, only on a machine with no system keychain. Elsewhere they are in the keychain under `latere-cli model key`. |
| `config.json` | Your auto-upgrade choice. |
| `update-check.json` | When the CLI last checked for a release, and what it found. |

In your user configuration directory, under `latere/`:

| File | What it holds |
|------|---------------|
| `topos-provider.json` | The model provider `latere topos --local` uses, and an Anthropic API key when you chose one. |
| `claude.json` | The Claude sign-in `latere topos login` saved. |

`latere review` writes each run's logs to
`$XDG_STATE_HOME/latere/reviews/<repo-key>/` and prunes old ones; see
[review.md](review.md#review-log-location).
