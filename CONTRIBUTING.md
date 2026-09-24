# Contributing

This file is for people changing the `latere` CLI. How to use it is in the
[README](README.md) and [`docs/`](docs/). Every bug fix ships with a test
that fails without it, and every change is one small commit whose message
says why.

## Building and testing

You need Go 1.27 or newer and git.

```sh
go run ./cmd/latere --help   # run from source
make binary                  # build ./latere
go test ./...                # the unit and package suites
make build                   # the local pre-commit gate
make check                   # the whole shared bar, as CI runs it
```

`make build` runs module tidiness, compilation, formatting, the
standard-library modernization check, the suite with `go vet`, the
vulnerability scan, and the spec lint. `make check` is `go tool lateregate`,
the quality bar every Latere Go repository shares, pinned in `go.mod`:
`go tool lateregate list` names its gates and `go tool lateregate <gate>`
runs one. What each gate asserts for this repository is in
`.lateregate.yaml`, including any waiver and its expiry. CI runs the same
gates through the reusable workflow in `latere-ai/ci`, so a gate that fails
there fails the same way on a laptop.

Wire the hooks once per clone with `make hooks`. The pre-commit hook checks
that staged Go files are formatted and hold no code the standard library
already covers; the pre-push hook lints the packages the push changes.

### Tests and credentials

The `internal/commands` suite runs with a temporary config directory and
token file and overrides any inherited `LATERE_AUTH_TOKEN_FILE`. A test that
needs a saved login creates synthetic credentials with `t.Setenv` and
`t.TempDir`; no test may depend on the developer's own session. Network
tests talk to an `httptest` server.

The live end-to-end tests are opt-in and **skip silently without their
environment variables**, so a green `go test ./...` does not mean they ran:

| Test | Enabled by | What it exercises |
|------|------------|-------------------|
| `TestFamilyE2E` | `LATERE_FAMILY_E2E=1` | Every product against production with your signed-in identity. `LATERE_FAMILY_E2E_WRITE=1` adds the write paths, which spend money on real model calls and clean up after themselves; `LATERE_FAMILY_E2E_LOGOUT=1` ends by revoking your session. |

## How the code is organized

| Path | What it holds |
|------|---------------|
| `cmd/latere` | `main`, and the end-to-end tests that run the built binary against test servers |
| `internal/commands` | the command tree: one file per command group, and the terminal interfaces for Topos |
| `internal/api` | the Cella client, the saved login, token refresh, and the actor-token mint every product credential comes from |
| `internal/config` | where the CLI keeps its files: `$XDG_CONFIG_HOME/latere`, or `~/.config/latere` |
| `internal/drive` | the Drive client |
| `internal/modelkey` | the model key for the Latere API: creation at auth, and storage in the keychain or a file |
| `internal/reviews` | where `latere review` writes its logs, and their retention |
| `internal/upgrade` | release discovery, verification, self-replacement, and the daily check |

A product command never presents the login token. It asks auth for a token
addressed to that one product, through one function in `internal/api`, so a
new command inherits the rule instead of restating it. A model call presents
the model key of `internal/modelkey` instead.
[`docs/login-and-tokens.md`](docs/login-and-tokens.md) states the rule as
a user sees it.

## Specs

A change to a command's surface starts as a spec in [`specs/`](specs/README.md)
with testable acceptance criteria. The implementation follows the spec, and
a divergence is recorded in the spec rather than left in the code.
`make spec-lint` checks that the tree agrees with itself.

## Releasing

Every tag has a section in [`CHANGELOG.md`](CHANGELOG.md), and that section
is the body of the GitHub release. Write under `Unreleased` as work lands.
`go tool lateregate release vX.Y.Z` turns `Unreleased` into the tag's
section, commits, tags, and pushes; a tag without a section is refused at
the pre-push hook and fails the release workflow. That workflow is the
shared CLI release pipeline in `latere-ai/ci`: it runs the suite under the
race detector on Linux and macOS, then GoReleaser builds the archives from
`.goreleaser.yaml` and publishes the GitHub release with `checksums.txt`
and the changelog section as its body. `install.sh` and `latere upgrade`
both install the newest published release, so a tag whose workflow failed
reaches nobody.

## Writing

Every sentence the `latere` CLI emits or carries is written for one reader,
and the register follows the reader:

- User, a person or a coding harness: every line the CLI prints, the
  `--json` `message` and `hint` fields, the docs. Short and plain: what
  happened and what to do next, naming a command or a page, never a package,
  a function, a table, or a Kubernetes object.
- Contributor, someone changing the `latere` CLI: specs, this file, package
  documentation, commit messages, source comments. Precise, in the project's
  own terms, with the reason a design is what it is.
- Developer, someone debugging a running system: the `details` field,
  logs. Exact and complete: object, operation, observed
  value, expected value, and the underlying error.

An error has one code, one fixed user sentence in `message`, and one
developer detail in a separate field shown only on request. The canonical
statement, worked examples, and the review checklist are in the
[registers document](https://github.com/latere-ai/pkg/blob/main/docs/writing/registers.md)
in pkg. The rule applies to new text and to reviews; existing text is fixed
as it is touched.
