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

`extend` defaults to 24 hours. An explicit `--hours` value must be positive;
`--deadline` overrides it when supplied and must be a nonempty RFC3339 timestamp
in the future. Invalid lifetime values are rejected before sending a request.

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

If printing a background command or detached run ID as text fails, the CLI
returns an error that includes the ID and states that the job has already started.

A start response missing its command or run ID is also an error. The job may
already be running, so the CLI does not retry the start after such a response.

`run --follow` starts the command, streams logs, and exits with the command's status:

```sh
latere cella run <name|id> --follow -- sh -lc 'go test ./...'
```

Log reads and streaming commands fail if logs cannot be written to stdout.
Following stops immediately on an output error.

One-shot execution uses the backend's atomic disposable-run API:

```sh
latere cella run --ephemeral --rm -- sh -lc 'go test ./...'
latere cella run --ephemeral --rm --timeout 900 -- sh -lc 'npm test'
```

Synchronous one-shot runs also exit with an error if the command's stdout or
stderr cannot be written locally.

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

Import recognizes compressed tar archives by their contents even without a
filename extension. Named compressed files that are not tar archives are copied
unchanged.
Legacy V7 tar archives are also detected without an extension.

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
Tar imports accept plain tar and gzip (`.tar.gz`, `.tgz`), bzip2 (`.tar.bz2`,
`.tbz`, `.tbz2`), and XZ (`.tar.xz`, `.txz`) compression. Compressed tar is
also accepted on stdin. Decompression streams to the server; no extracted
copy is stored locally.

`--input` requires a regular file (symlinks to regular files are accepted).
To read a tar stream from a pipe, redirect it to stdin or use `--input -`.
Named pipes, devices, and directories are rejected before an upload starts.

To upload files or directory trees while preserving their paths:

```sh
latere cella upload <name|id> ./dist config.json --dest /workspace
```

Upload checks every source before sending data. Regular files, empty files,
and symlinks to regular files are supported. Devices, named pipes, and
directory symlinks are rejected, including inside directory trees.

Uploading `.` puts the current directory's contents directly in the destination.
Uploading `..` preserves the parent directory's name. Paths containing symlinks
follow the local filesystem's meaning of `..` when selecting files.

Upload paths and import archive names preserve quotes, Unicode, percent signs,
and line breaks.

Upload and import report success only after all file data has been sent.
Upload also checks that the server's file and byte counts match the transfer.
If the server responds early, the command reports an error instead
of confirming a transfer that did not finish.

For upload and import, `--timeout 0` disables the HTTP timeout; negative values
are rejected before sending a request.

## Configuration

| Setting | Purpose |
|---------|---------|
| `--api-url` | Override the Cella API URL for a command. |
| `SANDBOX_API_URL` | Default Cella API URL. |
