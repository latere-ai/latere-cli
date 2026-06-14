# latere

Command-line interface for the Latere product family — one binary for [Cella](https://cella.latere.ai) sandboxes and [Lux](https://lux.latere.ai) model access.

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

`latere` checks for new releases at most once a day and prints a one-line notice when one is available. To update:

```sh
latere upgrade            # download and install the latest release
latere upgrade --check    # report whether a newer release exists, without installing
latere upgrade --auto on  # auto-upgrade on the next run when a new release is available
latere upgrade --auto off # turn auto-upgrade back off
```

`upgrade` verifies the release checksum and replaces the running binary in place. If `latere` lives somewhere you cannot write (for example a system-wide `PREFIX=/usr/local` install), it tells you to re-run the installer instead. Auto-upgrade and the daily notice are skipped for `go install`/dev builds, in CI, and when output is not a terminal. Set `LATERE_NO_UPDATE_CHECK=1` to silence the check entirely.

> Self-update is unavailable on Windows (the running binary is locked); download the latest archive from the [releases page](https://github.com/latere-ai/latere-cli/releases/latest) instead.

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

## Products

| Product | What it does | Guide |
|---------|--------------|-------|
| **Cella** | Named sandboxes (ephemeral or persistent): create, exec, shell, logs, file transfer, MCP server. | [docs/cella.md](docs/cella.md) |
| **Lux** | Call language models on your identity, no key to allocate: discovery, SDK enablement, chat, usage, and serving your own local models (Ollama/vLLM/LM Studio/llama.cpp/MLX) through Lux. | [docs/lux.md](docs/lux.md) |

```sh
latere cella apply -f sandbox.yaml
latere lux chat --model openai/gpt-4o-mini "Say hi"
```

## Development

```sh
go test ./...
go run ./cmd/latere --help
```

## License

MIT. See [LICENSE](./LICENSE).
