# latere

Command-line interface for the Latere product family: one binary for [Cella](https://cella.latere.ai) sandboxes, [Lux](https://lux.latere.ai) model access, and adversarial code review.

## Install

```sh
curl -fsSL https://latere.ai/install.sh | sh
```

The installer writes to `$HOME/.local/bin` by default, so normal installs do not require `sudo`. If that directory is not on your `PATH`, the installer prints the line to add to your shell profile.

Other install paths:

```sh
# Pin a version
curl -fsSL https://latere.ai/install.sh | sh -s -- v0.2.5

# System-wide install
curl -fsSL https://latere.ai/install.sh | PREFIX=/usr/local sh

# Build from source
go install github.com/latere-ai/latere-cli/cmd/latere@latest
```

Release binaries are attached to GitHub releases for Linux, macOS, and Windows on amd64 and arm64.

The installer resolves the latest version from the GitHub releases redirect rather than the rate-limited GitHub API, so it works from networks behind shared NAT. If version resolution still fails (restricted network or proxy), pin a version explicitly with the `sh -s -- vX.Y.Z` form above.

## Stay up to date

`latere` keeps itself current. **Auto-upgrade is on by default**: it checks for new releases at most once a day, and the next time you run a command after a new release appears, it updates itself in place before running your command. You can also drive it manually:

```sh
latere upgrade            # install the latest release now
latere upgrade v0.2.29    # install a specific release — this is how you roll back
latere upgrade --check    # report whether a newer release exists, without installing
latere upgrade --auto off # turn auto-upgrade off
latere upgrade --auto on  # turn it back on
```

If an auto-upgraded release turns out to be broken, roll back with `latere upgrade <previous-version>` and optionally `latere upgrade --auto off` to stay put. Every install verifies the release archive's checksum before replacing the binary.

Auto-upgrade and the daily notice are skipped for `go install`/dev builds, in CI, when output is not a terminal, and when `latere` lives somewhere you cannot write (for example a system-wide `PREFIX=/usr/local` install) — there `latere upgrade` tells you to re-run the installer instead. Set `LATERE_NO_UPDATE_CHECK=1` to silence the check entirely.

> Self-update is unavailable on Windows (the running binary is locked); download the archive you want from the [releases page](https://github.com/latere-ai/latere-cli/releases) instead.

## Sign in

```sh
latere auth login
```

`latere auth login` starts the OAuth2 device-code flow against `auth.latere.ai`. It prints a URL and user code, waits for browser approval, then saves the token to `~/.config/latere/token.json`. One sign-in unlocks every product below.

```sh
latere auth whoami
latere auth print-token
latere auth logout

# CI or dashboard-minted tokens
latere auth login --token <token>
```

### Switching the active organization

```sh
latere auth org switch <org-uuid>   # scope the saved token to <org-uuid>
latere auth org switch --personal   # scope the saved token to the personal context
```

`org switch` uses the auth service's refresh-token grant: no device-code re-prompt, the saved refresh token is exchanged for a new access token scoped to the chosen org. The on-disk token file is rewritten in place.

| Setting | Purpose |
|---------|---------|
| `--auth-url` | Override the auth URL for `latere auth login`. |
| `LATERE_TOKEN_FILE` | Token file path, default `~/.config/latere/token.json`. |

## Git with Drive

Drive (`drive.latere.ai`) serves repo workspaces over git smart-HTTP. Signing in is all it takes — `latere auth login` also wires git's credential helper for `drive.latere.ai`, so plain `git clone` authenticates with your saved login, no token in the URL:

```sh
latere auth login

git clone https://drive.latere.ai/git/me/<repo>.git          # your personal repos
git clone https://drive.latere.ai/git/<org-slug>/<repo>.git  # org repos
```

The helper is scoped to `drive.latere.ai` only; credentials for every other host keep flowing through your existing helpers. Fetch and clone need read access to the repo workspace, push needs write access.

If you'd rather manage the git config yourself, these are the escape hatches:

```sh
latere auth login --no-git             # sign in without touching git config
latere git-credential setup            # wire the helper explicitly
latere git-credential setup --remove   # undo the wiring
```

In CI, skip the helper and embed a token in the URL instead:

```sh
git clone https://x:${LATERE_TOKEN}@drive.latere.ai/git/<org-slug>/<repo>.git
```

## Products

| Product | What it does | Guide |
|---------|--------------|-------|
| **Cella** | Named sandboxes (ephemeral or persistent): create, exec, shell, logs, file transfer. | [docs/cella.md](docs/cella.md) |
| **Lux** | Call language models on your identity, no key to allocate: discovery, SDK enablement, chat, usage, and serving your own local models (Ollama/vLLM/LM Studio/llama.cpp/MLX) through Lux. | [docs/lux.md](docs/lux.md) |
| **Review** | Adversarial review of your latest Claude Code session: a proposer defends the diff, critics attack it through Lux, unresolved attacks surface. | [docs/review.md](docs/review.md) |
| **Topos** | Remote coding-assistant sessions on the Latere agent platform: start an interactive session, detach and reattach with state intact, approve tool calls inline, or run one prompt headless with `-p`. | [docs/topos.md](docs/topos.md) |

```sh
latere cella apply -f sandbox.yaml
latere lux chat --model openai/gpt-4o-mini "Say hi"
latere review
```

## Development

```sh
go test ./...
go run ./cmd/latere --help
```

## License

MIT. See [LICENSE](./LICENSE).
