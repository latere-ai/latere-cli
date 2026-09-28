# Repositories

`latere repos` creates, lists, and looks up Git repositories on Latere Code
(`code.latere.ai`). A repository lives under an owner, your handle or an
organization's short name, and is addressed as `<owner>/<name>`. Every
command acts in your current context: your personal account, or the
organization `latere org` selected.

Run `latere login` first (see the [main README](../README.md#sign-in)).

## Create a repository

```sh
latere repos create alice/notes            # private
latere repos create alice/site --public    # readable and clonable by anyone
latere repos create alice/notes --json     # the platform's answer as JSON
```

The owner must be one you may create under in the context you are in:

| Owner | Context | Who may create |
|---|---|---|
| your handle | your personal account, `latere org --personal` | you, once you have claimed a handle in the console |
| an organization's short name | that organization, `latere org <org-id>` | its owners and admins |

A name is letters, digits, and `.` `_` `-`, up to 64 of them, and is unique
under its owner. A repository is private unless you pass `--public`; you can
change that later on the repository's **Access** tab in the console. An owner
holds a limited number of repositories, and a create past it is refused
`repository_limit`.

The repository is created empty, and the command prints its id and the two
clone URLs. Push a first commit over git:

```sh
git remote add origin https://code.latere.ai/alice/notes.git
git push -u origin main
```

`latere login` configured git to sign in to `code.latere.ai` with your login,
so the push needs nothing more (see
[Git with Latere Code](../README.md#git-with-latere-code)).

## List and look up

```sh
latere repos list                 # the context's repositories, and those shared with you
latere repos list --json
latere repos get alice/notes      # one repository: id, visibility, your role, clone URLs
latere repos get alice/notes --json
```

`list` shows the repositories your current context's owner holds and your
role on each. In your personal context it also shows, under **Shared with
you**, the repositories other people own and have given you access to. `get`
finds a repository among the same rows, so a repository of an organization
you are not in the context of is not found there; switch with `latere org`.

## When a command is refused

A refusal prints the platform's code and its sentence, and a detail line when
the platform gives one, and exits 1:

```text
forbidden: That owner name is not yours to create under.
detail: a repository lives under your handle, or under the name of the organization your token is active in as owner or admin
```

| Code | What it means |
|---|---|
| `forbidden` | the owner is not yours to create under in this context, or the account cannot create repositories |
| `conflict` | the name is taken under this owner, or the owner name belongs to somebody else |
| `repository_limit` | the owner holds as many repositories as it may |
| `invalid_request` | the name is not one a repository may take |
| `unauthorized` | the token was refused; run `latere login` again |
| `origo_unreachable`, `origo_refused` | Latere Code did not answer or refused, and nothing was created; run the command again in a few minutes |
| `origo_unavailable` | this deployment does not create repositories yet |

## Where the commands go

The commands call the platform at `https://platform.latere.ai`, which records
a repository and creates it at Latere Code in one step. Each command presents
a token auth mints from your login for the platform's audience,
`api.latere.ai`, valid for five minutes; your login token itself is never
sent to the platform.

| Variable | Default | What it does |
|---|---|---|
| `LATERE_PLATFORM_URL` | `https://platform.latere.ai` | The platform's address. `--platform-url` overrides it. |
| `LATERE_PLATFORM_TOKEN` | none | A bearer to present as given instead of minting one, for a script or a machine that holds its own token. |
| `CODE_HOST` | `code.latere.ai` | The git host the printed clone URLs name. |
