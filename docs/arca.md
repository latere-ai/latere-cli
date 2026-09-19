# latere arca

`latere arca` works with your files on Latere's storage service: upload, download, list, trash, version history, and sharing, from the terminal, with the login you already have. Run `latere login` first (see the [main README](../README.md#sign-in)).

Repositories are not here. Their history lives on Latere Code (`code.latere.ai`), which `latere login` wires git for (see [Git with Latere Code](../README.md#git-with-latere-code)).

## Paths and spaces

Paths are plane-rooted exactly as in the API: `files/…` for your documents, `workspaces/…` for folders a sandbox mounts. Agent memory is a convention under `files/memory/…`, not a plane of its own.

Commands operate in your personal space by default. `--owner` takes the subject of another space you can reach, such as `https://auth.latere.ai|9ab3`. Every listing prints a subject in full, so you can copy one out of a listing and send it back.

## The verbs

```sh
latere arca ls [prefix]               # list (default files/); --long for size/mtime/checksum; --trashed for the trash
latere arca get <path>                # download; -o <file|->; --version N for a historical version
latere arca put <src> [path]          # upload (default files/<basename>); '-' reads stdin
latere arca mv <src> <dst>            # move/rename within the space
latere arca rm <path>                 # trash; --permanent to hard-delete; --version N to prune one version
latere arca restore <path>            # undo: from trash, or --version N to roll back in place
latere arca history <path>            # version history
latere arca share <prefix> --link     # sharing: also --to <recipient>, --public;
latere arca shares [--inbox]          #   list what you shared (or what's shared with you)
latere arca unshare <share-id>        #   revoke
```

Every command takes `--json` for machine-readable output on stdout.

File, trash, history, and share listings fetch every page before printing results. If a listing repeats a pagination cursor, the command stops with an error and leaves stdout empty.

Downloads reject partial or unexpected success responses before writing file bytes, preserving existing destination files. Valid empty files are supported.

Moves verify the destination in the server's response. Version restores and trash restores verify the path. Incomplete or mismatched receipts return an error with the outcome unknown; the CLI does not retry the operation.

`rm --permanent` also purges a file that is already in the trash. If that purge fails, the command reports the purge error so you can distinguish a missing file from a permission or service failure. A missing, null, or negative purge count leaves the deletion outcome unknown; the CLI reports an error without retrying.

If the service accepts a deletion or share revocation without confirming completion, the CLI reports that the outcome is unknown and does not retry.

## Uploads

Files up to 16 MiB stream in a single request; larger files go through an upload session automatically (16 MiB parts, four in flight, up to 16 GiB). Uploading from stdin is single-request and capped at 100 MB.

Uploads verify that the server acknowledges the requested path and byte count, including zero for empty files. Missing or mismatched acknowledgments return an error with the upload outcome unknown; the CLI does not retry the upload.

Upload sessions also verify the destination before sending file data. A missing or mismatched destination abandons the session without uploading any parts.

No write needs a condition, and either flag adds one: `--create-only` fails if the file already exists, and `--if-match <checksum>` overwrites only if the file has not changed since you read it. Read the current checksum from `latere arca ls --long`.

## Sharing

`share` grants access to everything under a path prefix.

- `--to <recipient>` grants one recipient, named by the subject a listing showed you or by an address your organization resolves, with `--permission read|write|manage`.
- `--link` mints a tokenized address, printed on stdout, that anyone holding it can read from.
- `--public` makes the prefix readable without an address of their own.

A link and the public are read-only; passing `--permission` with either is refused before anything is sent. The address a mint prints is the API path the token is redeemed at, under the API origin.

The token is answered once and never again. If it cannot be written to stdout, the command exits with an error that includes the share ID. The share still exists; the CLI does not retry its creation.

Share creation verifies the returned ID, status, permission, recipient, path prefix, and explicit owner, and a token grant must carry its token and its address. Invalid receipts report an unknown creation outcome without retrying or printing an unverified address.

`shares` lists grants to a recipient and token grants together: they are two resources in the API, and one listing is everyone who can reach the space. The recipient column carries the subject on a grant, and `link` or `public` on a token grant. `unshare` takes either kind of id.

## Settings

| Setting | Purpose |
|---------|---------|
| `--api-url` / `ARCA_API_URL` | Override the API origin (default `https://api.latere.ai`). The issuer tokens are minted at follows it unless `--auth-url` or `AUTH_URL` names one. |
| `--token` / `LATERE_ARCA_TOKEN` | Present this bearer instead of the saved login (CI). |
| `--owner` | Space to operate in: `me` (default), or the subject of another space. |
