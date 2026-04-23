# latere

The command-line interface for the Latere product family.

## Install

```sh
curl -fsSL https://latere.ai/install.sh | sh
```

Default install location is `$HOME/.local/bin`, so no `sudo` is required.
If that directory is not on your `$PATH`, the installer prints the line
you need to add to your shell profile.

Other options:

```sh
# pin a version
curl -fsSL https://latere.ai/install.sh | sh -s -- v0.1.0

# system-wide install (will prompt for sudo)
curl -fsSL https://latere.ai/install.sh | PREFIX=/usr/local sh

# Homebrew (coming soon)
# brew install latere-ai/tap/latere
```

Binaries for Linux, macOS, and Windows (amd64 + arm64) are attached to every
GitHub release.

## Usage

```sh
latere --version
latere --help
```

Today the binary prints a deliberate "not implemented yet" error for every
backing command. The surface is frozen so you can script against it; the
implementations land alongside the auth and sandbox service rollouts.

```sh
latere auth login
latere auth whoami
latere auth logout

latere sandbox create [--tier ephemeral|persistent] [--image IMG] [--name N]
latere sandbox list
latere sandbox get <name|id>
latere sandbox rename <name|id> <new-name>
latere sandbox start <name|id>
latere sandbox stop <name|id>
latere sandbox delete <name|id>

latere exec <name|id> -- <cmd>...
```

Planned surface and product context: [latere.ai/cella](https://latere.ai/cella).

## Build from source

```sh
go install github.com/latere-ai/latere-cli/cmd/latere@latest
```

## License

MIT. See [LICENSE](./LICENSE).
