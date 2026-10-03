# Environments

Latere Environments runs your workloads: each one is a workspace at
`/workspace` plus the compute that runs your commands, and it reaches only
the hosts its egress boundary admits. `latere environments` talks to the
Environments API at `https://api.latere.ai/v1/environments`. Run
`latere login` first (see [Sign in](../README.md#sign-in)).

Until v0.16 this command group was `cella`, with the alias `sandbox`. For
one release those words exit 1 and name this group; then they are removed.

## Quick start

Describe a workload in a manifest and apply it:

```sh
cat > workload.yaml <<'YAML'
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

latere environments apply -f workload.yaml --wait
latere environments exec demo -- sh -lc 'echo hello && pwd'
latere environments shell demo
```

The manifest's `kind: Sandbox` and `apiVersion: cella.latere.ai/v1beta1`
are the API's names for a workload. The
[manifest reference](https://platform.latere.ai/docs/environments/manifest)
lists every field. `apply` sends the manifest as written, YAML or JSON, and
reads it from stdin with `-f -`. A manifest that names its workload is
applied under that name, so applying it again updates the same workload
rather than creating a second one.

Run one command in a disposable workload that is deleted afterwards:

```sh
latere environments run --ephemeral --rm -- sh -lc 'echo hello && pwd'
```

## Creating and waiting

A create answers as soon as the workload is recorded, usually in phase
`Pending`, and the workload starts after. `apply` prints it and says how to
wait. With `--wait` the server holds the answer until the workload runs or
fails, for at most ten minutes, or for `--wait=DURATION` (up to 1h):

```sh
latere environments apply -f workload.yaml            # answers Pending
latere environments apply -f workload.yaml --wait     # answers Running, or Failed
latere environments apply -f workload.yaml --wait=2m
```

A workload that fails to start exits 1 with its reason, such as an image
that cannot be pulled or resources no node can place. A hold that ends
while the workload is still starting is not a failure: the CLI prints its
phase, and `latere environments get` reads it later.

A workload's phase is one of `Pending`, `Queued`, `Starting`, `Running`,
`Stopped`, `Failed`, `Lost`, `Recovering` and `Deleting`, shown with its
reason when it has one.

## Images and egress

A workload runs an image from the platform's catalog: `base`, the default,
or `gui` for a desktop. The platform enforces each workload's egress
boundary, and holds an organization's workloads to its allowlist. A
manifest sets the boundary under `spec.network.egress`:

```yaml
spec:
  network:
    egress:
      allowedHosts: [api.latere.ai, github.com, proxy.golang.org]
```

A disposable workload from `run --ephemeral` sets no boundary and takes the
platform's default for your account.

## Lifecycle

```sh
latere environments list                  # your workloads; --json for the API's objects
latere environments get <name|id>         # the full object, as JSON
latere environments start <name|id>
latere environments stop <name|id>        # the workspace is kept across a stop
latere environments delete <name|id>      # removes the workspace; export first
```

A workload's name and resources are fixed when it is created. Its
lifetime comes from `spec.lifecycle` in the manifest: `autoStop` stops it
after that much idle time, `ttl` deletes it that long after its creation,
and `autoDelete` deletes it once stopped.

## Commands

`exec` runs a command in an existing workload, waits for it, writes its
standard output and standard error to yours, and exits with its code:

```sh
latere environments exec <name|id> -- sh -lc 'go test ./...'
latere environments exec <name|id> --cwd app --env DEBUG=1 -- npm test
latere environments exec <name|id> --timeout 30m -- make build
```

The output arrives when the command ends, each stream cut at one mebibyte,
and the CLI notes a cut. The command's standard input is empty. `--cwd`
takes a path under `/workspace`, `--env KEY=VALUE` repeats, and
`--timeout` (default 10 minutes, at most 1h) ends the command with exit
code 124.

An interactive terminal, with the image's shell or the command after `--`
(`attach` is an alias):

```sh
latere environments shell <name|id>
latere environments shell <name|id> -- python3
```

The terminal follows your window's size, and the CLI exits with the
shell's exit code.

`logs` reads the output of the workload's main process, the command its
manifest runs:

```sh
latere environments logs <name|id>
latere environments logs <name|id> --tail 100
latere environments logs <name|id> --follow --since 2026-09-26T10:00:00Z
```

### One-off runs

`run --ephemeral --rm` creates a disposable workload, waits for it to run,
runs one command, and deletes the workload when the command ends, fails, or
is interrupted:

```sh
latere environments run --ephemeral --rm -- sh -lc 'go test ./...'
latere environments run --ephemeral --rm --timeout 900 --cpu 2 --memory 4Gi -- npm test
latere environments run --ephemeral --rm --json -- uname -a
```

A one-off run also takes `--image`, `--disk` in GiB, `--cpu` and `--memory`
as Kubernetes quantities, `--env`, `--cwd`, `--timeout` in seconds (default
600, at most 3600), and `--json`. The workload stops after 15 minutes idle
and is deleted two hours after its creation, so one the CLI could not
delete does not linger. When the delete fails, the CLI prints the command
that finishes it.

### Exit codes

`exec`, `shell` and a one-off run exit with the remote command's code, 0 to
255. A workload that failed to start, a refusal from the API, or a code
outside that range exits 1 with the reason on stderr. When a one-off run's
command failed and its workload could not be deleted, the exit code is the
command's and the failed delete is printed to stderr.

## Files

Read and change one file or directory inside a workload. Every path is at
or below `/workspace`, and a relative path is resolved under it:

```sh
latere environments ls <name|id> /workspace
latere environments cat <name|id> out.log
latere environments mkdir <name|id> build
latere environments mv <name|id> a.txt b.txt
latere environments rm <name|id> old                   # recursive
echo hi | latere environments write <name|id> note.txt
latere environments write <name|id> app.tar -f app.tar
```

`ls` prints one entry per line: the mode in octal, the size in bytes, and
the name, with a trailing slash on a directory. `write` creates missing
parent directories, and a write that ends early leaves the previous file
whole.

Move trees in and out as tar streams:

```sh
# Export paths under /workspace to a file (stdout without -o)
latere environments export <name|id> dist -o dist.tar

# Export from another directory
latere environments export <name|id> --src-dir /workspace/results logs -o results.tar

# Import a tar stream from stdin
tar -cf - ./src | latere environments import <name|id> --dest /workspace

# Import a tar or zip archive, or one regular file
latere environments import <name|id> --input payload.tar --dest /workspace
latere environments import <name|id> --input payload.zip --dest /workspace
latere environments import <name|id> --input data.jsonl --dest /workspace

# Upload files and directories, keeping their paths
latere environments upload <name|id> ./dist config.json --dest /workspace
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

The Environments API has no counterpart for these commands of the earlier
hosted sandbox service. Each one exits 1 and says why:

| Command | Instead |
|---|---|
| `policy`, `policy list` | Set the egress boundary in the manifest's `spec.network.egress`. |
| `rename` | A workload's name is fixed at creation. |
| `extend`, `convert` | Set `spec.lifecycle` (`autoStop`, `ttl`, `autoDelete`) in the manifest. |
| `resize` | Apply a new workload with the resources it needs. |
| `run <name> -- CMD`, `run --follow`, `run --detach`, `run status`, `run logs`, `run cancel`, `wait` | `exec` runs a command in an existing workload and waits for it. |

`--credential`, `--idempotency-key` and `shell --session` are gone as
well: a manifest mounts secrets through `spec.secrets`, and an apply by
name is already idempotent. `logs` now reads the workload's main process,
not a background command.

## Settings

| Setting | Purpose |
|---------|---------|
| `--api-url` / `LATERE_ENVIRONMENTS_URL` | The Environments API base URL, including its `/v1/environments` path. `https://api.latere.ai/v1/environments` by default. |
| `LATERE_ENVIRONMENTS_TOKEN` | Present this bearer to the Environments API instead of minting one from your login. |
| `AUTH_URL` | The issuer that mints the Environments token. By default it is inferred from the API's host: `api.latere.ai` gives `auth.latere.ai`. |

Both variables had other names before v0.16. An old name set without its
replacement is refused, and the message names the new variable, so a
script that points at another deployment does not reach production instead.
