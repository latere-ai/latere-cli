# Apps

`latere apps` creates apps on the Latere platform, decides who can open them,
changes their address, and shows what your pushes built. An app is a git repository and an address:
a push to a branch builds a preview of it, and a pushed tag that starts with
`v` releases that commit to the app's address. A new app is private until you
open it, and you can invite people to it by email or give it a password.
Every command acts in your current context: your personal account, or the
organization `latere org` selected.

Run `latere login` first (see the [main README](../README.md#sign-in)). It
also lets git sign in to `code.latere.ai`, which is where you push.

## From a repository to an address

```sh
cd my-site
latere apps create hello                   # create the app and add the remote latere
git push latere main && latere apps logs -f   # build a preview and follow it
git push latere v1.0.0                    # release that commit to the address
```

`latere apps create` prints the app's address, the address of its newest
preview, its push URL and who can open it, then the next command to run:

```text
app:        hello
address:    https://hello.latere.site
preview:    https://next--hello.latere.site
push:       https://code.latere.ai/u-9b2c1d6e/hello.git
visibility: private

Only you and the people you invite can open it, each after signing in with a Latere account.
Open it to anyone who has its address, or invite people by email:
  latere apps visibility unlisted hello
  latere apps share hello <email>...

Added the git remote latere.

Next:
  git push latere main
```

## Create an app

```sh
latere apps create                               # a generated slug
latere apps create "Hello World"                 # a slug derived from the name: hello-world
latere apps create --slug hello                  # this slug or a refusal
latere apps create hello --visibility unlisted   # open to anyone who has the address
latere apps create --remote deploy               # add the remote under another name
latere apps create --no-remote --json            # no remote; the app as JSON
```

The slug is the app's address. It is 1 to 40 lowercase letters, digits and
single hyphens, and a slug under three characters needs a plan that allows
short slugs.

A new app is private. `--visibility unlisted` creates it open to anyone who
has its address instead, and [Who can open an app](#who-can-open-an-app)
covers both and how to change them later. The command prints the visibility
the app was created with.

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

## Who can open an app

Who can open an app's address and its previews is one of three choices:

| Who can open it | What the app is | Who opens the app |
|---|---|---|
| Only people invited | `private`, without a password | You, or the members of the organization that owns the app, and the people you invite by email. Each signs in with a Latere account first. |
| Anyone with the password | `private`, with a password | The same people, signed in, and anyone who types the app's password at its address, with no Latere account. |
| Anyone with the link | `unlisted` | Anyone who has the address, without signing in. The app is kept out of search engines. |

A new app starts on Only people invited. You choose between `private` and
`unlisted` when you create the app or at any time after, and a password is
set with a command of its own:

```sh
latere apps visibility unlisted          # anyone with the link
latere apps visibility private hello     # only people invited
latere apps password hello               # anyone with the password
```

```text
app:        hello
address:    https://hello.latere.site
visibility: unlisted

Anyone with the link: it opens without signing in, and is kept out of search engines.
```

`latere apps visibility` removes the app's password when it has one, with
either value, and says so. `private` means only people invited, so the
people who used the password no longer get in. An `unlisted` app opens for
anyone with the link, where a password decides nothing, and one left set
would open the app again the next time it is private.

The visibility is changed whatever becomes of the password, which the
platform keeps apart from it. When the platform cannot be asked whether the
app has a password, or does not remove the one it has, the command says so
in one line on standard error, with the command that removes a password,
and exits 0:

```text
The password of hello, if it has one, could not be checked or removed (cannot authenticate to the platform: not logged in; run `latere login`). Remove one with: latere apps password --remove hello
```

A `public` app opens for anyone too, and search engines may list it. An app
becomes `public` when it is shown in the gallery at latere.site. You ask for
that from the app's Share in the chat, and it is reviewed first, so the
command line does not take `public`. An app that is public reads as public
in every command. Changing the visibility takes the app out of the gallery,
and ends a request to show it there that still waits for its review.

Changing who can open an app takes the app's owner, or an owner or admin of
the organization that owns it.

### Open a private app with a password

A password lets people open a private app without a Latere account: a
class, a client, a family. Whoever opens the app's address or one of its
previews is asked for the password, and whoever types it opens the app. You,
your organization's members and the people you invited keep opening it by
signing in.

```sh
latere apps password                              # asks for it twice, without showing it
latere apps password hello --generate             # makes one and prints it once
latere apps password hello --stdin < password.txt # reads one line of standard input
latere apps password hello --remove               # removes it
```

```text
app:        hello
address:    https://hello.latere.site
visibility: private

The password of hello is set.
Anyone with the password: whoever types it at the app's address opens the app, with no Latere account.
You and the people you invite still open the app by signing in with a Latere account.
Send people the app's address and the password. Neither is emailed.

The generated password, printed this once:
k7m2p-9xw4h-tq3bn-8fjyc
```

The password is never a flag's value or an argument, which a shell history
would keep. It comes from the first of these that applies:

| From | What is read |
|---|---|
| `--generate` | The command makes one: 20 letters and digits in four groups, without the ones that look alike. |
| `--stdin` | One line of standard input. Its line ending is removed and nothing else, so a space is part of the password. |
| A prompt | On a terminal, the command asks twice before it does anything else, and shows nothing you type. The two answers must be the same. |

Without a terminal the command needs `--stdin` or `--generate`, and says so.

A generated password is printed once, on the last line of the output. The
platform keeps no password in a form it can show again, so copy it then; a
lost password is replaced, never recovered. `--json` prints an object with
`app` and `password`: `{"set": true}`, with `"generated"` holding the value
only when `--generate` made it. A password you typed or piped is in no
output.

The platform says how short and how long a password may be, and the command
holds a password to that before it sends it: one outside the bounds is
refused with the bound it missed, and nothing changes. Any characters count,
and nothing is trimmed.

Setting a password makes the app private when it is not, and the command
says so: an app that anyone with the link opened then opens with the
password, or by signing in. Running the command again replaces the
password. Replacing or removing it ends every visit made with the one
before within about a minute.

Nothing is emailed: send people the app's address and the password
yourself. One password opens the app for everyone who has it, so to let in
one person and not another, invite them by email instead.

`--remove` removes the password and leaves the visibility as it is, and it
is no error when the app has none. `latere apps access` says whether a
password is set, and since when.

Where the platform does not offer passwords yet, the command says so, exits
1 and changes nothing.

### Invite people to a private app

```sh
latere apps share ada@example.com grace@example.com   # inside the app's repository
latere apps share hello ada@example.com               # by slug
```

```text
Invited to hello:
  ada@example.com

Invitations are not emailed. Send the people you invited the app's address:
  https://hello.latere.site
Each of them opens it after signing in with a Latere account whose verified email is the address you invited.
```

Each person you invite opens the app at its address and at its previews, and
sees nothing else of it: not its deploys, its logs or its settings.
Invitations are not emailed, so send the address yourself. The person opens
it and signs in with a Latere account whose verified email is the address
you invited, or creates an account with that address. The first sign-in with
that address accepts the invitation, and from then on the app opens for that
account.

An argument without an `@` in first position is the slug, and every other
argument is an address. Your own address and an address already invited are
skipped, and the command says so. The platform bounds how many people one app
can have invited and how many invitations one person makes in a day, and a
refusal names the bound.

An unlisted or public app is already open to anyone who has its address. You
can still invite people to one: they stay invited, and open the app once it
is private.

### See who can open it

```sh
latere apps access            # who can open the app, and the people invited
latere apps access hello --json
```

```text
app:        hello
address:    https://hello.latere.site
visibility: private

Anyone with the password, set 2026-10-08 12:00 UTC: whoever types it at the app's address opens the app, with no Latere account.
You and the people you invite still open the app by signing in with a Latere account.

EMAIL                 STATE    NAME         INVITED
ines@example.com      active   Ines Moreau  5d
pilot@harbor.example  pending  -            3h
```

The first lines say which of the three choices the app is on. A password is
said with the moment it was set, never the password itself, and an app
without one reads `Only people invited`. An invitation is `pending` until
the person signs in at the app with the invited address, and `active` from
then on, with their name. `--json` prints an object with `app` and `access`,
the app and what the platform keeps of who can open it, each as its API
answers it: the list of people, and `password`, which is the moment a
password was set or `null`.

### Take an invitation back

```sh
latere apps unshare hello pilot@harbor.example
```

An invitation no one accepted is gone at once, and a person who already
opened the app is refused shortly after. An address that is not invited is
reported, and the others are still taken back.

Inviting, listing and taking back take whoever may change the app's
settings: its owner, or an owner or admin of the organization that owns it.
The same people set and remove its password.

## See what you have

```sh
latere apps list              # slug, state, visibility, the release production serves, address
latere apps show              # one app: who can open it, addresses, production, newest preview, push URL
latere apps deploys           # the deploys, newest first: id, ref, status, preview address, age
```

`list` and `show` print the app's visibility, and do not read whether a
private app also has a password: `latere apps access` says that.

Each takes `--json` and prints the API's own objects. `show --json` prints an
object with `app`, `release` (the release production serves, or null) and
`preview` (the newest preview deploy, or null).

A command that takes `[slug]` reads it from the git remote `latere` of the
repository you run it in, so inside the app's repository you can leave it
out.

## Follow a build

```sh
latere apps logs                  # the newest deploy's build log so far
latere apps logs -f               # follow it until the build ends
latere apps logs hello 5d2f8a1c   # one deploy, by an id prefix of 8 or more characters
latere apps logs --json           # each line as JSON
```

`--follow` exits 0 when the build succeeds and 1 when it fails or is canceled,
printing the failure's code, message and what to do about it:

```text
deploy 5d2f8a1c failed in app: build_failed: The build command failed.
Read the build log; the failing line is near the end.
```

A push records its deploy a moment after it returns. So inside the app's
repository, `latere apps logs -f` follows the deploy of the commit `HEAD`
names, and waits up to a minute for it to appear. That is what makes
`git push latere main && latere apps logs -f` one step.

The deploy of a release builds nothing: it serves the build of the preview
whose commit the tag names. Its log is that preview's, so the command says so
and prints the preview's build log instead, followed or not:

```text
Deploy 02c415ed released v1.0.0 from preview cbc11a51 without a build; its build log:
```

Color codes in the log are printed as they are on a terminal and removed when
the output goes to a file or a pipe.

## Change an app's address

```sh
latere apps rename hello-world          # the app of this repository
latere apps rename hello-world hello    # the app hello, from anywhere
latere apps rename hello-world --json   # the app as JSON
```

An app's slug is its address, so a new slug moves the app: it and its
previews answer at `https://hello-world.latere.site` at once. The command
prints the new address and push URL, and what became of the old one:

```text
app:        hello-world
address:    https://hello-world.latere.site
preview:    https://next--hello-world.latere.site
push:       https://code.latere.ai/u-9b2c1d6e/hello-world.git

https://hello.latere.site stopped working, and the app's previews moved with it. Nothing sends a visitor on to the new address, so links to the old one stop working.
The slug hello stays held for your account for seven days: nobody else can take it, and `latere apps rename hello hello-world` takes it back.

Set the git remote latere to the new push URL.
```

The old address stops working at once and is not redirected, so tell the
people who have a link to it. The old slug is held for seven days: nobody
else can take it in that time, and renaming the app back takes it again.
The app is the same app: its repository and history, its deploys and
releases, the people invited to it and its password stay as they were.

The push URL changes with the slug. In a git repository whose remote
`latere` is the app's old push URL, the command sets that remote to the new
one. A remote `latere` that points anywhere else is left as it is, and the
command prints the line that sets it; so does a run outside a repository:

```text
git remote set-url latere https://code.latere.ai/u-9b2c1d6e/hello-world.git
```

Run that line in every other project that pushes to the app.

The new slug follows the rules of [a slug at a create](#create-an-app). One
that breaks a rule is refused before anything is sent, and the app's own
slug changes nothing. Renaming takes the app's owner, or an owner or admin
of the organization that owns it, and the command asks no question first: a
rename is undone by renaming back within the hold.

When a rename is refused, nothing has changed. The command says why and
exits 1:

| Code | What it means |
|---|---|
| `slug_taken` | another app has the slug, or it is held for the account that gave it up |
| `slug_reserved` | the platform keeps the slug for itself |
| `slug_held` | the slug is the name of a repository deleted recently, which is still held |
| `invalid_slug` | the slug breaks a rule, or is under three characters on a plan that does not allow short slugs; the message names the rule |
| `repository_name_taken` | the app's owner already has a repository with the slug as its name, which the app's repository would take |
| `repository_unavailable` | the app's repository could not be renamed; run the command again in a few minutes |
| `forbidden` | you may not rename this app |
| `app_not_found` | your current context has no app with this slug; `latere org` switches it |

## Delete an app

```sh
latere apps delete hello          # asks you to type the slug back
latere apps delete hello --yes
```

The app's address stops serving it at once. The slug stays held for your
account for seven days, and `latere apps create --slug hello` within them
restores the app and its repository.

## Where the commands go

The commands call `https://api.latere.ai/v1/apps`. Each command presents a
token auth mints from your login for the Apps API, valid for five minutes;
your login token itself is never sent there.

| Variable | Default | What it does |
|---|---|---|
| `LATERE_APPS_URL` | `https://api.latere.ai/v1/apps` | The Apps API's address, including its `/v1/apps` path. `--api-url` overrides it. |
| `LATERE_APPS_TOKEN` | none | A bearer to present as given instead of minting one. |

The people invited to an app and its password are kept by the platform, so
`latere apps visibility`, `password`, `access`, `share` and `unshare` read
the app at the Apps API and then call `https://platform.latere.ai`, with a
token minted for the platform alone. `visibility` changes the app at the
Apps API whatever the platform answers, so a script that holds a bearer for
the Apps API alone still changes it, with one line on standard error for
the password it could not check.

| Variable | Default | What it does |
|---|---|---|
| `LATERE_PLATFORM_URL` | `https://platform.latere.ai` | The platform's address. `--platform-url` overrides it. |
| `LATERE_PLATFORM_TOKEN` | none | A bearer to present to the platform as given instead of minting one. |
