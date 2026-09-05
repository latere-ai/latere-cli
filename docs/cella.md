# Cella

[Cella](https://cella.latere.ai) provides named sandboxes — ephemeral enough to throw away or persistent enough to keep. Run `latere login` first (see the [main README](../README.md#sign-in)).

## Quickstart

Describe a Cella in a YAML file and apply it:

```sh
cat > sandbox.yaml <<'YAML'
apiVersion: cella.latere.ai/v1            # Schema version.
kind: Sandbox
metadata:
  name: demo                              # Optional.
spec:
  image: ghcr.io/latere-ai/sandbox-base:latest
  tier: ephemeral                         # Or "persistent" to keep it.
  lifecycle:
    autoStop: 15m
YAML

latere cella apply -f sandbox.yaml
latere cella exec demo -- sh -lc 'echo hello && pwd'
latere cella shell demo
```

The same YAML works in the dashboard's YAML tab and over the
public API with `Content-Type: application/yaml`. Full field
reference: <https://cella.latere.ai/docs/cella/manifest>.

Run a one-shot disposable command. The backend creates an ephemeral
cella, runs the command, returns output and timing, then deletes the
cella:

```sh
latere cella run --ephemeral --rm -- sh -lc 'echo hello && pwd'
```

Run a background job and follow logs:

```sh
CMD=$(latere cella run demo -- sh -lc 'sleep 5 && echo done')
latere cella logs demo "$CMD" --follow
```

## Lifecycle

```sh
latere cella apply -f sandbox.yaml
latere cella list
latere cella get <name|id>
latere cella rename <name|id> <new-name>
latere cella start <name|id>
latere cella stop <name|id>
latere cella delete <name|id>
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

`create` and `run` accept repeatable `--credential <catalog-key>` to attach
client trust-plane credentials by catalog key. `--env KEY=VALUE` is only for
non-secret configuration. `run` also accepts `--cwd /path`; one-shot runs also
accept `--image`, `--disk`, `--timeout`, `--detach`, and `--json`.

Foreground execution, `wait`, and followed logs return the remote command's
exit code (0–255). Synchronous one-shot runs do this with `--json` too.
If no valid exit code is available, or the run fails or is cancelled despite
an exit code of 0, the CLI exits 1 and prints a diagnostic to stderr. This
includes cleanup failures after a successful command.

```sh
latere cella run demo --credential llm-primary -- sh -lc 'curl http://127.0.0.1:8888/upstreams/llm-primary/v1/models'
```

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

ZIP imports preserve file paths and directory entries, including empty directories.

To upload files or directory trees while preserving their paths:

```sh
latere cella upload <name|id> ./dist config.json --dest /workspace
```

Upload checks every source before sending data. Regular files, empty files,
and symlinks to regular files are supported. Devices, named pipes, and
directory symlinks are rejected, including inside directory trees.

## Configuration

| Setting | Purpose |
|---------|---------|
| `--api-url` | Override the Cella API URL for a command. |
| `SANDBOX_API_URL` | Default Cella API URL. |
