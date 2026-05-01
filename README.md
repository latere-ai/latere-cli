# latere

Command-line interface for the Latere product family. Today it primarily drives [Cella](https://cella.latere.ai): named sandboxes that can be ephemeral enough to throw away or persistent enough to keep.

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

## Sign in

```sh
latere auth login
```

`latere auth login` starts the OAuth2 device-code flow against `auth.latere.ai`. It prints a URL and user code, waits for browser approval, then saves the token to `~/.config/latere/token.json`.

Useful auth commands:

```sh
latere auth whoami
latere auth print-token
latere auth logout
```

For CI or dashboard-minted tokens:

```sh
latere auth login --token <token>
```

During device-code login, the CLI attempts to exchange the auth-issued token for a Cella-issued bearer token. If exchange is temporarily unavailable during a rollout, it falls back to the auth token.

## Quickstart

Create an ephemeral cella and run a command:

```sh
latere cella create --name demo --tier ephemeral
latere cella exec demo -- sh -lc 'echo hello && pwd'
latere cella shell demo
```

Run a one-shot disposable command. The backend creates an ephemeral
cella, runs the command, returns output and timing, then deletes the
cella:

```sh
latere cella run --ephemeral --rm -- sh -lc 'echo hello && pwd'
```

Create a persistent workspace:

```sh
latere cella create --name work --tier persistent --disk 10
latere cella stop work
latere cella start work
```

Run a background job and follow logs:

```sh
CMD=$(latere cella run demo -- sh -lc 'sleep 5 && echo done')
latere cella logs demo "$CMD" --follow
```

## Cella lifecycle

```sh
latere cella create
latere cella list
latere cella get <name|id>
latere cella rename <name|id> <new-name>
latere cella start <name|id>
latere cella stop <name|id>
latere cella delete <name|id>
```

Create flags:

```sh
latere cella create \
  --name work \
  --image ghcr.io/latere-ai/sandbox-base:main \
  --tier persistent \
  --disk 10 \
  --auto-stop-minutes 30 \
  --auto-delete-hours 24 \
  --ttl 12h \
  --env GOFLAGS=-count=1 \
  --policy default
```

Tier changes:

```sh
# Push an ephemeral cella's delete deadline forward
latere cella extend <name|id> --hours 24
latere cella extend <name|id> --deadline 2026-04-27T12:00:00Z

# Keep the workspace until explicit delete
latere cella convert <name|id> --to persistent

# Return to a disposable lifetime
latere cella convert <name|id> --to ephemeral --hours 12
```

`latere sandbox ...` remains as an alias for older scripts, but new usage should prefer `latere cella ...`.

## Commands and logs

Interactive shell opens a long-lived PTY WebSocket, matching the dashboard terminal protocol:

```sh
latere cella shell <name|id>
```

Foreground execution streams output and exits with the command's status:

```sh
latere cella exec <name|id> -- sh -lc 'go test ./...'
```

Background execution prints a command id:

```sh
latere cella run <name|id> -- sh -lc 'sleep 30 && echo done'
```

`run --follow` starts the command, streams logs, and exits with the command's status:

```sh
latere cella run <name|id> --follow -- sh -lc 'go test ./...'
```

One-shot execution uses the backend's atomic disposable-run API:

```sh
latere cella run --ephemeral --rm -- sh -lc 'go test ./...'
latere cella run --ephemeral --rm --timeout 900 -- sh -lc 'npm test'
```

Detached one-shot execution returns immediately with a run id. The
backend keeps the result and log tail for later inspection:

```sh
RUN=$(latere cella run --ephemeral --rm --detach -- sh -lc 'sleep 30 && echo done')
latere cella run status "$RUN"
latere cella run logs "$RUN" --follow
latere cella run cancel "$RUN"
```

Inspect output and status:

```sh
latere cella logs <name|id> <command_id>
latere cella logs <name|id> <command_id> --cursor 1024
latere cella logs <name|id> <command_id> --follow
latere cella wait <name|id> <command_id> --timeout 600
```

`run` accepts repeatable `--env KEY=VALUE` for non-secret configuration and
`--cwd /path`. One-shot runs also accept `--image`, `--disk`, `--timeout`,
`--detach`, and `--json`.

## Files

Cella file transfer uses tar streams:

```sh
# Export selected paths from /workspace
latere cella export <name|id> ./dist -o dist.tar

# Export from another directory
latere cella export <name|id> --src-dir /workspace ./dist -o dist.tar

# Import from stdin
tar -cf - ./src | latere cella import <name|id> --dest /workspace

# Import from a tar file
latere cella import <name|id> --input payload.tar --dest /workspace

# Import one regular file
latere cella import <name|id> --input data.jsonl --dest /workspace

# Import a zip archive
latere cella import <name|id> --input payload.zip --dest /workspace
```

## MCP

`latere cella mcp` runs a stdio MCP server using the same token file as the CLI.
By default it exposes a focused coding-agent surface over existing Cella sandboxes:
`Sandboxes`, `Read`, `Write`, `Edit`, `Bash`, `Monitor`, `Glob`, `Grep`, `Upload`,
and `Download`.

Example MCP config:

```json
{
  "mcpServers": {
    "latere-cella": {
      "command": "latere",
      "args": ["cella", "mcp"]
    }
  }
}
```

Alias sandboxes at startup when an MCP host should work across more than one target:

```json
{
  "mcpServers": {
    "latere-cella": {
      "command": "latere",
      "args": ["cella", "mcp", "--sandbox", "frontend=my-frontend", "--sandbox", "backend=sbx_..."]
    }
  }
}
```

Every action tool takes a `sandbox` selector. The selector can be a configured
alias, a sandbox id, or a sandbox slug. File paths are relative to the selected
sandbox's backend-resolved working directory; the MCP server does not assume
`/workspace`.

Tool `env` fields are literal non-secret variables. Credentials should come
from the Cella trust-plane catalog configured for the selected sandbox.

Lifecycle and command-management tools are opt-in:

```sh
latere cella mcp --surface=management
latere cella mcp --surface=all
```

Management tools use the same PascalCase naming style as the agent tools:

- `CreateSandbox`, `ListSandboxes`, `GetSandbox`
- `StartSandbox`, `StopSandbox`, `ExtendSandbox`, `ConvertSandbox`, `DeleteSandbox`
- `RunCommand`, `WaitCommand`, `CommandLogs`, `KillCommand`
- `ExportFiles`, `ImportFiles`

## Configuration

| Setting | Purpose |
|---------|---------|
| `--api-url` | Override the Cella API URL for a command. |
| `--auth-url` | Override the auth URL for `latere auth login`. |
| `SANDBOX_API_URL` | Default Cella API URL. |
| `LATERE_TOKEN_FILE` | Token file path, default `~/.config/latere/token.json`. |

## Development

```sh
go test ./...
go run ./cmd/latere --help
```

## License

MIT. See [LICENSE](./LICENSE).
