# Cella

[Cella](https://cella.latere.ai) runs named sandboxes: ephemeral enough to
throw away, or persistent enough to keep. Each one is a workspace disk
plus the compute that runs your commands. Run `latere login` first (see
[Sign in](../README.md#sign-in)).

`latere sandbox` is an alias for `latere cella`, kept for older scripts.

## Quick start

Describe a sandbox in a manifest and apply it:

```sh
cat > sandbox.yaml <<'YAML'
apiVersion: cella.latere.ai/v1            # Schema version.
kind: Sandbox
metadata:
  name: demo                              # Optional; the server picks one if omitted.
spec:
  image: ghcr.io/latere-ai/sandbox-base:latest
  tier: ephemeral                         # Or "persistent" to keep it.
  lifecycle:
    autoStop: 15m                         # Stop the compute after this much idle time.
YAML

latere cella apply -f sandbox.yaml
latere cella exec demo -- sh -lc 'echo hello && pwd'
latere cella shell demo
```

The same manifest works in the web console's YAML tab and over the API
with `Content-Type: application/yaml`. The
[manifest reference](https://platform.latere.ai/docs/cella/manifest) lists
every field. `apply` also reads the manifest from stdin with `-f -`, and
`--idempotency-key` makes a retried create return the original result
instead of creating a second sandbox.

Run a one-off command in a disposable sandbox that is removed afterwards:

```sh
latere cella run --ephemeral --rm -- sh -lc 'echo hello && pwd'
```

Run a background job and follow its logs:

```sh
CMD=$(latere cella run demo -- sh -lc 'sleep 5 && echo done')
latere cella logs demo "$CMD" --follow
```

## Lifecycle

```sh
latere cella list                           # your sandboxes; --json for scripts
latere cella get <name|id>                  # the full record, as JSON
latere cella rename <name|id> <new-name>    # same workspace and id, new name
latere cella start <name|id>
latere cella stop <name|id>
latere cella delete <name|id>               # removes the workspace; export first
latere cella policy list                    # policy profiles you can choose in spec.policy
```

An ephemeral sandbox stops when idle and is deleted after a deadline; a
persistent one stays until you delete it.

```sh
# Push an ephemeral sandbox's delete deadline forward
latere cella extend <name|id> --hours 24
latere cella extend <name|id> --deadline 2026-12-01T12:00:00Z

# Keep the workspace until you delete it
latere cella convert <name|id> --to persistent

# Return to a disposable lifetime; --hours is required
latere cella convert <name|id> --to ephemeral --hours 12

# Grow a persistent sandbox's workspace disk; it never shrinks
latere cella resize <name|id> --disk-gb 50
```

`extend` defaults to 24 hours. `--hours` must be positive; `--deadline`
overrides it and must be an RFC 3339 time in the future. An invalid value
is refused before any request is sent.

A policy decides what a sandbox may do at run time, such as its network
shape and whether it needs Cella's credential sidecar. A manifest with no
`spec.policy` gets the default. If a create fails because the chosen policy
requires the sidecar, pick a policy whose sidecar column says `no`, or ask
an administrator to set up the sidecar for your account.

## Commands and logs

An interactive shell (`attach` is an alias):

```sh
latere cella shell <name|id>
```

A command in the foreground streams its output and exits with the
command's status:

```sh
latere cella exec <name|id> -- sh -lc 'go test ./...'
```

A command in the background prints a command id:

```sh
latere cella run <name|id> -- sh -lc 'sleep 30 && echo done'
latere cella run <name|id> --env DEBUG=1 --cwd /workspace/app -- npm test
```

`run --follow` starts the command, streams its logs, and exits with its
status:

```sh
latere cella run <name|id> --follow -- sh -lc 'go test ./...'
```

Read, follow, or wait for a background command:

```sh
latere cella logs <name|id> <command_id>
latere cella logs <name|id> <command_id> --cursor 1024
latere cella logs <name|id> <command_id> --follow
latere cella wait <name|id> <command_id> --timeout 600
```

`wait --timeout` takes a positive number of seconds. `--env KEY=VALUE` is
for configuration that is not secret. For a credential, `run` takes a
repeatable `--credential <catalog-key>`, which attaches a credential from
the trust plane's catalog by its key:

```sh
latere cella run demo --credential llm-primary -- \
  sh -lc 'curl http://127.0.0.1:8888/upstreams/llm-primary/v1/models'
```

### One-off runs

`--ephemeral --rm` creates a disposable sandbox for one command, runs it,
returns its output and timing, and deletes the sandbox:

```sh
latere cella run --ephemeral --rm -- sh -lc 'go test ./...'
latere cella run --ephemeral --rm --timeout 900 --cpu 2 --memory 4Gi -- sh -lc 'npm test'
```

A one-off run also takes `--image`, `--disk` (GB, default 1), `--cpu` and
`--memory` as Kubernetes quantities, `--timeout` in seconds (default 600),
and `--json`. `--timeout` bounds the command; the CLI allows extra time for
creating and cleaning up the sandbox.

`--detach` returns at once with a run id. The service keeps the result and
the tail of the log for later:

```sh
RUN=$(latere cella run --ephemeral --rm --detach -- sh -lc 'sleep 30 && echo done')
latere cella run status "$RUN"
latere cella run logs "$RUN" --follow
latere cella run cancel "$RUN"
```

### Exit codes and failures

Foreground `exec`, `wait`, followed logs, and a synchronous one-off run
(with or without `--json`) exit with the remote command's code, 0 to 255.
When no valid code is available, or the run failed or was canceled even
though the command returned 0, the CLI exits 1 and prints the reason to
stderr. That includes a cleanup failure after a successful command: the
output and the JSON result are still printed, and the exit code is the
command's own nonzero code, or 1.

The CLI does not report success it cannot confirm:

- If a background command id or a detached run id cannot be printed, the
  error includes the id and says the job has already started.
- A start response with no command or run id is an error. The job may
  already be running, so the CLI does not retry the start.
- `run status` and `run cancel` must name the requested run and its state;
  a missing or mismatched answer is an error.
- Reading or following logs stops with an error when stdout cannot be
  written, and a synchronous one-off run does the same when its stdout or
  stderr cannot be written.

## Files

Read and change files inside a sandbox:

```sh
latere cella ls <name|id> /workspace
latere cella cat <name|id> /workspace/out.log
latere cella mkdir <name|id> /workspace/build
latere cella mv <name|id> /workspace/a.txt /workspace/b.txt
latere cella rm <name|id> /workspace/old            # recursive
echo hi | latere cella write <name|id> /workspace/note.txt
latere cella write <name|id> /workspace/app.tar -f app.tar
```

`write` takes a file or stdin of at most 10 MiB and stops reading as soon
as the input passes the limit. Use `upload` for larger files.

Move trees in and out as tar streams:

```sh
# Export paths under /workspace to a file (stdout without -o)
latere cella export <name|id> ./dist -o dist.tar

# Export from another directory
latere cella export <name|id> --src-dir /workspace/results logs -o results.tar

# Import a tar stream from stdin
tar -cf - ./src | latere cella import <name|id> --dest /workspace

# Import a tar or zip archive, or one regular file
latere cella import <name|id> --input payload.tar --dest /workspace
latere cella import <name|id> --input payload.zip --dest /workspace
latere cella import <name|id> --input data.jsonl --dest /workspace

# Upload files and directories, keeping their paths
latere cella upload <name|id> ./dist config.json --dest /workspace
```

`import` extracts plain tar and gzip (`.tar.gz`, `.tgz`), bzip2
(`.tar.bz2`, `.tbz`, `.tbz2`), and XZ (`.tar.xz`, `.txz`) compressed tar,
recognized by content, so a file without an extension or a compressed
stream on stdin works too; old V7 tar archives are recognized as well.
Decompression streams to the service and no extracted copy is kept
locally. A zip archive keeps its paths and directory entries, including
empty directories. A compressed file that is not a tar archive, and any
other regular file, is copied as one file.

`--input` takes a regular file, or a symlink to one; use `--input -` or
stdin for a pipe. An empty `--input`, a named pipe, a device, or a
directory is refused before anything is sent.

`upload` checks every source before it sends anything. It takes regular
files, empty files, and symlinks to regular files, and refuses devices,
named pipes, and symlinks to directories, including inside a tree.
Uploading `.` puts the current directory's contents directly in the
destination; uploading `..` keeps the parent directory's name. A path
through a symlink follows the local file system's meaning of `..`. Quotes,
Unicode, percent signs, and line breaks in paths and archive names are
kept.

`upload` and `import` report success only after every byte was sent and
the service's receipt matches: `upload` checks the file and byte counts,
and `import` checks the archive name and the tar byte count after any
decompression or zip conversion. A missing or mismatched receipt, or a
service that answers before the transfer finished, is an error.
`--timeout` sets the transfer's HTTP timeout (default 5 minutes for
`upload`, 30 minutes for `import`); `0` removes it, and a negative value
is refused.

`cat` and `export` refuse a partial or unexpected success response before
they write anything, so an existing export file is left intact and stdout
stays empty. An empty file is a valid answer.

## Settings

| Setting | Purpose |
|---------|---------|
| `--api-url` / `SANDBOX_API_URL` | The Cella API, `https://cella.latere.ai` by default. |
| `LATERE_CELLA_TOKEN` | Present this bearer to Cella instead of minting one from your login. |
