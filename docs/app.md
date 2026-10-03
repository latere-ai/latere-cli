# Apps

`latere app` creates apps on the Latere platform and shows what your pushes
built. An app is a git repository and an address: a push to a branch builds a
preview of it, and a pushed tag that starts with `v` releases that commit to
the app's address. Every command acts in your current context: your personal
account, or the organization `latere org` selected.

Run `latere login` first (see the [main README](../README.md#sign-in)). It
also lets git sign in to `code.latere.ai`, which is where you push.

## From a repository to an address

```sh
cd my-site
latere app create hello                   # create the app and add the remote latere
git push latere main && latere app logs -f   # build a preview and follow it
git push latere v1.0.0                    # release that commit to the address
```

`latere app create` prints the app's address, the address of its newest
preview, and its push URL, then the next command to run:

```text
app:        hello
address:    https://hello.latere.site
preview:    https://next--hello.latere.site
push:       https://code.latere.ai/u-9b2c1d6e/hello.git

Added the git remote latere.

Next:
  git push latere main
```

## Create an app

```sh
latere app create                     # a generated slug
latere app create "Hello World"       # a slug derived from the name: hello-world
latere app create --slug hello        # this slug or a refusal
latere app create --remote deploy     # add the remote under another name
latere app create --no-remote --json  # no remote; the app as JSON
```

The slug is the app's address. It is 1 to 40 lowercase letters, digits and
single hyphens, and a slug under three characters needs a plan that allows
short slugs. An app is public.

In a git repository the command adds the push URL as the remote `latere`. If
the repository already has a remote of that name for another app, the command
refuses and creates nothing: pass `--remote <name>` to add the new app under
another name, or `--no-remote`.

When a create is refused, the command says why and exits 1:

| Code | What it means |
|---|---|
| `slug_taken` | another app has the slug |
| `slug_held` | the slug was deleted recently and is held for its previous owner; the message says until when |
| `slug_reserved` | the platform keeps the slug for itself |
| `invalid_slug` | the slug breaks a rule; the message names it |
| `slug_generation_failed` | no free slug was found; run the command again or pass `--slug` |
| `repository_create_failed`, `repository_unavailable` | the app's repository could not be created or restored, and nothing was created; run the command again |
| `forbidden` | your current context may not create an app; `latere org` switches it |

## See what you have

```sh
latere app list              # slug, state, the release production serves, address
latere app show              # one app: addresses, production, newest preview, push URL
latere app deploys           # the deploys, newest first: id, ref, status, preview address, age
```

Each takes `--json` and prints the API's own objects. `show --json` prints an
object with `app`, `release` (the release production serves, or null) and
`preview` (the newest preview deploy, or null).

A command that takes `[slug]` reads it from the git remote `latere` of the
repository you run it in, so inside the app's repository you can leave it
out.

## Follow a build

```sh
latere app logs                  # the newest deploy's build log so far
latere app logs -f               # follow it until the build ends
latere app logs hello 5d2f8a1c   # one deploy, by an id prefix of 8 or more characters
latere app logs --json           # each line as JSON
```

`--follow` exits 0 when the build succeeds and 1 when it fails or is canceled,
printing the failure's code, message and what to do about it:

```text
deploy 5d2f8a1c failed in app: build_failed: The build command failed.
Read the build log; the failing line is near the end.
```

A push records its deploy a moment after it returns. So inside the app's
repository, `latere app logs -f` follows the deploy of the commit `HEAD`
names, and waits up to a minute for it to appear. That is what makes
`git push latere main && latere app logs -f` one step.

The deploy of a release builds nothing: it serves the build of the preview
whose commit the tag names. Its log is that preview's, so the command says so
and prints the preview's build log instead, followed or not:

```text
Deploy 02c415ed released v1.0.0 from preview cbc11a51 without a build; its build log:
```

Color codes in the log are printed as they are on a terminal and removed when
the output goes to a file or a pipe.

## Delete an app

```sh
latere app delete hello          # asks you to type the slug back
latere app delete hello --yes
```

The app's address stops serving it at once. The slug stays held for your
account for seven days, and `latere app create --slug hello` within them
restores the app and its repository.

## Where the commands go

The commands call `https://api.latere.ai/v1/apps`. Each command presents a
token auth mints from your login for the Apps API, valid for five minutes;
your login token itself is never sent there.

| Variable | Default | What it does |
|---|---|---|
| `LATERE_APP_URL` | `https://api.latere.ai/v1/apps` | The Apps API's address, including its `/v1/apps` path. `--api-url` overrides it. |
| `LATERE_APP_TOKEN` | none | A bearer to present as given instead of minting one. |
