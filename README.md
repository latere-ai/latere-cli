# latere

[![ci](https://github.com/latere-ai/latere-cli/actions/workflows/ci.yaml/badge.svg)](https://github.com/latere-ai/latere-cli/actions/workflows/ci.yaml)
[![release](https://img.shields.io/github/v/release/latere-ai/latere-cli)](https://github.com/latere-ai/latere-cli/releases)
[![go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

`latere` is the command-line interface for the Latere product family. You
sign in once, and one binary reaches every product with your identity:
[Cella](https://cella.latere.ai) sandboxes, [Lux](https://lux.latere.ai)
model access, [Topos](https://topos.latere.ai) agent sessions, git on
Latere Code (`code.latere.ai`), and adversarial review of a Claude Code
session. There is no API key to allocate and no second credential to
manage.

```sh
latere login
latere cella apply -f sandbox.yaml
latere lux invoke --model openai/gpt-4o-mini "Say hi"
latere topos --local
git clone https://code.latere.ai/<owner>/<repo>.git
```

## Install

```sh
curl -fsSL https://latere.ai/install.sh | sh
```

The installer supports Linux and macOS on amd64 and arm64. It writes to
`$HOME/.local/bin`, so it needs no `sudo`, and prints the line to add to
your shell profile when that directory is not on your `PATH`.

```sh
# Pin a version
curl -fsSL https://latere.ai/install.sh | sh -s -- vX.Y.Z

# Install system-wide
curl -fsSL https://latere.ai/install.sh | PREFIX=/usr/local sh

# Build from source
go install github.com/latere-ai/latere-cli/cmd/latere@latest
```

The installer finds the newest release through the GitHub releases
redirect rather than the rate-limited GitHub API, so it also works behind
shared NAT. If it still cannot resolve a version, for example behind a
restrictive proxy, pin one with the `sh -s -- vX.Y.Z` form. It checks the
archive against the release's `checksums.txt` when `shasum` is available.

Every [release](https://github.com/latere-ai/latere-cli/releases) carries
archives for Linux, macOS, and Windows on amd64 and arm64, and a
`checksums.txt`. On Windows, download the `.zip` from the releases page.

## Stay up to date

Auto-upgrade is on by default. `latere` checks for a new release at most
once a day, and the next time you run a command after one appears, it
replaces itself and then runs your command.

```sh
latere upgrade            # install the latest release now
latere upgrade v0.2.29    # install a specific release; this is how you roll back
latere upgrade --check    # report whether a newer release exists, without installing
latere upgrade --auto off # turn auto-upgrade off
latere upgrade --auto on  # turn it back on
```

If a release is broken, roll back with `latere upgrade <previous-version>`
and add `latere upgrade --auto off` to stay there. Every upgrade verifies
the archive against the release's checksum and its gzip integrity before it
replaces the binary. An empty binary, an archive or a binary over 200 MiB,
and an archive that expands past 400 MiB are refused, and the installed
executable is left as it was.

The daily check and auto-upgrade are skipped for `go install` and
development builds, when `CI` is set, when stderr is not a terminal, and
when `latere` lives in a directory you cannot write, such as a
`PREFIX=/usr/local` install; there `latere upgrade` tells you to run the
installer again. `LATERE_NO_UPDATE_CHECK=1` turns the check off entirely.
Self-upgrade is not available on Windows, where a running binary is
locked; download the release you want from the releases page instead.

## Sign in

```sh
latere login
```

`latere login` runs the OAuth 2.0 device-code flow against
`auth.latere.ai`: it prints a URL and a code, you approve in a browser and
choose your personal account or an organization, and the CLI saves one
login token to `~/.config/latere/auth-token.json`. Every product command
mints a short-lived token for that one product from it.
[`docs/login-and-tokens.md`](docs/login-and-tokens.md) explains what is
stored and how each product gets its credential.

```sh
latere whoami                 # the principal the saved login names
latere org                    # the active context
latere org <org-uuid>         # switch to an organization, without signing in again
latere org --personal         # switch to your personal account
latere logout                 # revoke the session and the model keys it created, then clear the login

latere login --token <token>  # save a token issued elsewhere, for CI
latere print-token            # print the login token, for scripts that call auth
```

## Git with Latere Code

Latere Code (`code.latere.ai`) hosts git repositories. `latere login` also
configures git's credential helper for that host, so a plain clone
authenticates with your saved login and no token goes in the URL:

```sh
latere login
git clone https://code.latere.ai/<owner>/<repo>.git
```

The helper answers for that host only; every other host keeps the helpers
you already have. Fetch and clone need read access, push needs write
access, and a public repository clones with no credential at all.

```sh
latere login --no-git                  # sign in without touching git config
latere git-credential setup            # configure the helper yourself
latere git-credential setup --remove   # remove it
```

In CI, sign the job in with a token and let the same helper answer:

```sh
latere login --token "$LATERE_TOKEN"
git clone https://code.latere.ai/<owner>/<repo>.git
```

## Products

| Command | What it does | Guide |
|---------|--------------|-------|
| `latere cella` | Sandboxes, ephemeral or persistent: create from a manifest, run commands, open a shell, read logs, and move files in and out. | [docs/cella.md](docs/cella.md) |
| `latere lux` | Call language models with your identity: discover models and rates, point a stock SDK at Lux, check usage, and serve a model running on your own machine through Lux. | [docs/lux.md](docs/lux.md) |
| `latere topos` | Coding-agent sessions. `--local` runs an agent on this machine against your files; without it, sessions run on the hosted platform, where you can detach and reattach, approve tool calls, or run one prompt headless. | [docs/topos.md](docs/topos.md) |
| `latere review` | Adversarial review of your latest Claude Code session: a proposer defends the diff, critics attack it through Lux, and unresolved attacks set the exit code. | [docs/review.md](docs/review.md) |
| `latere drive` | Files on Latere Drive. Drive has been retired and its address no longer answers, so these commands fail to connect. | [docs/drive.md](docs/drive.md) |

[`docs/configuration.md`](docs/configuration.md) lists every environment
variable the CLI reads and every file it keeps.

Shell completion comes from the binary: `latere completion <shell>` prints
the script for bash, zsh, fish, or PowerShell.

### For operators

`latere eval` manages model-evaluation suites on `eval.latere.ai`: a
manifest declares tasks crossed with a model and harness matrix, and
`latere eval apply -f suite.yaml` reconciles it, with `--dry-run` to see
the change first. `latere eval suites` and `latere eval cells --suite <id>`
list what exists. It is an administration tool and does not use your
login: it presents the token in `EVAL_ADMIN_TOKEN` or `--token`, which is
a token the issuer minted for the `eval` audience, held by a platform
administrator or a service account. A manifest is one YAML document of at
most 256 KiB after prompt files are inlined; a `file://` prompt is read
relative to the manifest, and a nonempty `prompt_text` takes precedence
over `prompt`. Apply never
deletes a cell and does not retry: when the response does not confirm the
suite, its status, and the dry-run mode, the command reports that the
outcome is unknown. `latere eval --help` has the rest.

## Compatibility

`latere` is released from git tags and is before 1.0. A command that moves
keeps its old spelling as a hidden alias, so `latere auth login` still
works after the session commands moved to the top level. Output formats
are not frozen: if a script parses the output, pin a version with
`latere upgrade vX.Y.Z` and `latere upgrade --auto off`, and prefer
`--json` where a command offers it.

## Contributing

[`CONTRIBUTING.md`](CONTRIBUTING.md) covers building, the test suites, the
quality gate, and how the code is organized. The design records behind
each command group are in [`specs/`](specs/README.md), and
[`CHANGELOG.md`](CHANGELOG.md) says what each release changed.

## License

MIT. See [LICENSE](./LICENSE).
