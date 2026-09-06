# latere drive

`latere drive` works with files on [Latere Drive](https://drive.latere.ai): upload, download, list, trash, version history, and sharing — from the terminal, with the login you already have. Run `latere login` first (see the [main README](../README.md#sign-in)).

Repo workspaces are a different plane: they are served over git, and plain `git clone https://drive.latere.ai/git/me/<repo>.git` already works after login (see [Git with Drive](../README.md#git-with-drive)).

## Paths and spaces

Paths are namespace-rooted exactly as in the Drive API: `files/…` (your documents), `memory/…` (agent memory, CAS-guarded), `repos/…` and `workspaces/…` (workspace-managed). Commands operate in your personal space by default; `--owner org` targets your active organization's space, and an explicit `u-<uuid>` / `o-<uuid>` targets any space you can see.

## The verbs

```sh
latere drive ls [prefix]              # list (default files/); --long for size/mtime/checksum; --trashed for the trash
latere drive get <path>               # download; -o <file|->; --version N for a historical version
latere drive put <src> [path]         # upload (default files/<basename>); '-' reads stdin
latere drive mv <src> <dst>           # move/rename within the space
latere drive rm <path>                # trash; --permanent to hard-delete; --version N to prune one version
latere drive restore <path>           # undo: from trash, or --version N to roll back in place
latere drive history <path>           # version history
latere drive share <prefix> --link    # sharing: also --to <email|principal-id>, --public;
latere drive shares [--inbox]         #   list what you shared (or what's shared with you)
latere drive unshare <share-id>       #   revoke
```

Every command takes `--json` for machine-readable output on stdout.

File, trash, history, and share listings fetch every page before printing results. If Drive repeats a pagination cursor, the command stops with an error and leaves stdout empty.

Downloads reject partial or unexpected success responses before writing file bytes, preserving existing destination files. Valid empty files are supported.

Moves verify the source and destination in the server's response. Version restores verify the file path and requested version. Trash restores verify the file path and completed restore status. Incomplete or mismatched receipts return an error with the outcome unknown; the CLI does not retry the operation.

`rm --permanent` also purges a file that is already in the trash. If that purge fails, the command reports the purge error so you can distinguish a missing file from a permission or service failure. A missing, null, or negative purge count leaves the deletion outcome unknown; the CLI reports an error without retrying.

If Drive accepts a deletion or share revocation without confirming completion, the CLI reports that the outcome is unknown and does not retry.

## Uploads

Files up to 16 MiB stream in a single request; larger files go through Drive's multipart plane automatically (16 MiB parts, four in flight, up to 16 GiB). Uploading from stdin is single-request and capped at 100 MB.

Uploads verify that the server acknowledges the requested path and byte count, including zero for empty files. Missing or mismatched acknowledgments return an error with the upload outcome unknown; the CLI does not retry the upload.

Multipart uploads also verify the session's destination before sending file data. A missing or mismatched destination aborts the session without uploading any parts.

Concurrent-write safety rides standard HTTP conditions: `--create-only` fails if the file already exists, and `--if-match <checksum>` overwrites only if the file hasn't changed since you read it (get the current checksum from `latere drive ls --long`). Writes under `memory/` require one of the two.

## Sharing

`share` grants access to everything under a path prefix. `--link` mints a read-only viewer URL (printed on stdout); `--to` grants a person by email or principal id (`--permission read|write|manage`); `--public` makes the prefix world-readable. In org spaces, shares may enter a `pending` state until an org admin approves them in the web app.

If the link cannot be written to stdout, the command exits with an error that includes the share ID. The share still exists; the CLI does not retry its creation.

Share creation verifies the returned ID, status, permissions, recipient type, path prefix, and explicit owner. Newly created active link, public, and email shares must also include a viewer URL. Invalid receipts report an unknown creation outcome without retrying or printing an unverified link. Pending shares remain valid while awaiting approval, as do existing email and public grants without viewer tokens.

## Settings

| Setting | Purpose |
|---------|---------|
| `--drive-url` / `DRIVE_API_URL` | Override the Drive base URL (default `https://drive.latere.ai`). |
| `--token` / `LATERE_DRIVE_TOKEN` | Present this bearer instead of the saved login (CI). |
| `--owner` | Space to operate in: `me` (default), `org`, `u-<uuid>`, `o-<uuid>`. |
