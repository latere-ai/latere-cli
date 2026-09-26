# Cella

Cella runs named sandboxes on the Latere platform. Each one is a
workspace at `/workspace` plus the compute that runs your commands, and it
reaches only the hosts its egress boundary admits. `latere cella` talks to
the Cella API at `https://api.latere.ai/v1/environments`. Run
`latere login` first (see [Sign in](../README.md#sign-in)).

`latere sandbox` is an alias for `latere cella`.

## Quick start

Describe a sandbox in a manifest and apply it:

```sh
cat > sandbox.yaml <<'YAML'
apiVersion: cella.latere.ai/v1beta1
kind: Sandbox
metadata:
  name: demo                    # Optional; the server picks one if omitted.
spec:
  image: base                   # base, or gui for a desktop.
  resources: {cpu: "2", memory: 4Gi, disk: 20Gi}
  lifecycle:
    autoStop: 15m               # Stop the compute after this much idle time.
YAML

latere cella apply -f sandbox.yaml --wait
latere cella exec demo -- sh -lc 'echo hello && pwd'
latere cella shell demo
```

The [manifest reference](https://platform.latere.ai/docs/cella/manifest)
lists every field. `apply` sends the manifest as written, YAML or JSON,
and reads it from stdin with `-f -`. A manifest that names its sandbox is
applied under that name, so applying it again updates the same sandbox
rather than creating a second one.

Run one command in a disposable sandbox that is deleted afterwards:

```sh
latere cella run --ephemeral --rm -- sh -lc 'echo hello && pwd'
```

## Creating and waiting

A create answers as soon as the sandbox is recorded, usually in phase
`Pending`, and the sandbox starts after. `apply` prints it and says how to
wait. With `--wait` the server holds the answer until the sandbox runs or
fails, for at most ten minutes, or for `--wait=DURATION` (up to 1h):

```sh
latere cella apply -f sandbox.yaml            # answers Pending
latere cella apply -f sandbox.yaml --wait     # answers Running, or Failed
latere cella apply -f sandbox.yaml --wait=2m
```

A sandbox that fails to start exits 1 with its reason, such as an image
that cannot be pulled or resources no node can place. A hold that ends
while the sandbox is still starting is not a failure: the CLI prints its
phase, and `latere cella get` reads it later.

A sandbox's phase is one of `Pending`, `Queued`, `Starting`, `Running`,
`Stopped`, `Failed`, `Lost`, `Recovering` and `Deleting`, shown with its
reason when it has one.

## Images and egress

A sandbox runs an image from the platform's catalog: `base`, the default,
or `gui` for a desktop. The hosted platform enforces each sandbox's egress
boundary, and holds an organization's sandboxes to its allowlist. A
manifest sets the boundary under `spec.network.egress`:

```yaml
spec:
  network:
    egress:
      allowedHosts: [api.latere.ai, github.com, proxy.golang.org]
```

A disposable sandbox from `run --ephemeral` sets no boundary and takes the
platform's default for your account.

## Lifecycle

```sh
latere cella list                  # your sandboxes; --json for the API's objects
latere cella get <name|id>         # the full object, as JSON
latere cella start <name|id>
latere cella stop <name|id>        # the workspace is kept across a stop
latere cella delete <name|id>      # removes the workspace; export first
```

A sandbox's name and resources are fixed when it is created. Its
lifetime comes from `spec.lifecycle` in the manifest: `autoStop` stops it
after that much idle time, `ttl` deletes it that long after its creation,
and `autoDelete` deletes it once stopped.

## Commands

`exec` runs a command in an existing sandbox, waits for it, writes its
standard output and standard error to yours, and exits with its code:

```sh
latere cella exec <name|id> -- sh -lc 'go test ./...'
latere cella exec <name|id> --cwd app --env DEBUG=1 -- npm test
latere cella exec <name|id> --timeout 30m -- make build
```

The output arrives when the command ends, each stream cut at one mebibyte,
and the CLI notes a cut. The command's standard input is empty. `--cwd`
takes a path under `/workspace`, `--env KEY=VALUE` repeats, and
`--timeout` (default 10 minutes, at most 1h) ends the command with exit
code 124.

An interactive terminal, with the image's shell or the command after `--`
(`attach` is an alias):

```sh
latere cella shell <name|id>
latere cella shell <name|id> -- python3
```

The terminal follows your window's size, and the CLI exits with the
shell's exit code.

`logs` reads the output of the sandbox's main process, the command its
manifest runs:

```sh
latere cella logs <name|id>
latere cella logs <name|id> --tail 100
latere cella logs <name|id> --follow --since 2026-09-26T10:00:00Z
```

### One-off runs

`run --ephemeral --rm` creates a disposable sandbox, waits for it to run,
runs one command, and deletes the sandbox when the command ends, fails, or
is interrupted:

```sh
latere cella run --ephemeral --rm -- sh -lc 'go test ./...'
latere cella run --ephemeral --rm --timeout 900 --cpu 2 --memory 4Gi -- npm test
latere cella run --ephemeral --rm --json -- uname -a
```

A one-off run also takes `--image`, `--disk` in GiB, `--cpu` and `--memory`
as Kubernetes quantities, `--env`, `--cwd`, `--timeout` in seconds (default
600, at most 3600), and `--json`. The sandbox stops after 15 minutes idle
and is deleted two hours after its creation, so one the CLI could not
delete does not linger. When the delete fails, the CLI prints the command
that finishes it.

### Exit codes

`exec`, `shell` and a one-off run exit with the remote command's code, 0 to
255. A sandbox that failed to start, a refusal from the API, or a code
outside that range exits 1 with the reason on stderr. When a one-off run's
command failed and its sandbox could not be deleted, the exit code is the
command's and the failed delete is printed to stderr.

## Files

Read and change one file or directory inside a sandbox. Every path is at
or below `/workspace`, and a relative path is resolved under it:

```sh
latere cella ls <name|id> /workspace
latere cella cat <name|id> out.log
latere cella mkdir <name|id> build
latere cella mv <name|id> a.txt b.txt
latere cella rm <name|id> old                   # recursive
echo hi | latere cella write <name|id> note.txt
latere cella write <name|id> app.tar -f app.tar
```

`ls` prints one entry per line: the mode in octal, the size in bytes, and
the name, with a trailing slash on a directory. `write` creates missing
parent directories, and a write that ends early leaves the previous file
whole.

Move trees in and out as tar streams:

```sh
# Export paths under /workspace to a file (stdout without -o)
latere cella export <name|id> dist -o dist.tar

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

`export` writes a file named by `-o` only once the whole archive has
arrived, so a transfer that fails part way leaves no archive that looks
complete.

`import` extracts plain tar and gzip (`.tar.gz`, `.tgz`), bzip2
(`.tar.bz2`, `.tbz`, `.tbz2`), and XZ (`.tar.xz`, `.txz`) compressed tar,
recognized by content, so a file without an extension or a compressed
stream on stdin works too. Decompression streams to the service and no
extracted copy is kept locally. A zip archive is converted to tar with its
paths and directory entries. A compressed file that is not a tar archive,
and any other regular file, is copied as one file. `--input` takes a
regular file; use `--input -` or stdin for a pipe.

`upload` checks every source before it sends anything. It takes regular
files and symlinks to regular files, and refuses devices, named pipes, and
symlinks to directories. Uploading `.` puts the current directory's
contents directly in the destination.

`--timeout` bounds the transfer (default 5 minutes for `upload`, 30
minutes for `import`); `0` removes the bound.

## Removed commands

The Cella API has no counterpart for these commands of the earlier hosted
sandbox service. Each one now exits 1 and says why:

| Command | Instead |
|---|---|
| `policy`, `policy list` | Set the egress boundary in the manifest's `spec.network.egress`. |
| `rename` | A sandbox's name is fixed at creation. |
| `extend`, `convert` | Set `spec.lifecycle` (`autoStop`, `ttl`, `autoDelete`) in the manifest. |
| `resize` | Apply a new sandbox with the resources it needs. |
| `run <name> -- CMD`, `run --follow`, `run --detach`, `run status`, `run logs`, `run cancel`, `wait` | `exec` runs a command in an existing sandbox and waits for it. |

`--credential`, `--idempotency-key` and `shell --session` are gone as
well: a manifest mounts secrets through `spec.secrets`, and an apply by
name is already idempotent. `logs` now reads the sandbox's main process,
not a background command.

## Settings

| Setting | Purpose |
|---------|---------|
| `--api-url` / `LATERE_CELLA_URL` | The Cella API base URL, including its `/v1/environments` path. `https://api.latere.ai/v1/environments` by default. |
| `LATERE_CELLA_TOKEN` | Present this bearer to Cella instead of minting one from your login. |
| `AUTH_URL` | The issuer that mints the Cella token. By default it is inferred from the API's host: `api.latere.ai` gives `auth.latere.ai`. |
