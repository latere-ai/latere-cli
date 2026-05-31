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
| **Lux** | Call language models on your identity, no key to allocate: discovery, SDK enablement, chat, usage. | [docs/lux.md](docs/lux.md) |

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
