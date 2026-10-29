# Memory

`latere memory` shows and changes what Latere remembers about you and your
projects. Latere keeps in mind what it learns in your conversations: how you
like to be answered, who you are, the topics and areas of your life you keep
coming back to, and the people you mention. Each memory is one line, in a
group, and says where it came from. The same memories are in Latere Chat's
Settings, and a change made here is there at once.

Run `latere login` first (see the [main README](../README.md#sign-in)).
Memory is kept for your personal account: in an organization's context the
commands are refused `memory_personal_only`, and `latere org --personal`
switches back.

## Where a memory is kept

| Kept for | Read by |
|---|---|
| You | every conversation outside a project, and a project's conversations too |
| a project | only that project's conversations |

A conversation outside a project saves what it learns to You. A
conversation in a project saves to that project, and never to You, so
nothing a project's conversation learns reaches another project. Move a
memory to You when it is about you rather than the project.

Memories are grouped:

| Group | `--group` | What belongs there |
|---|---|---|
| Preferences | `preference` | how to work with you: "Keep lists under five things" |
| Profile | `profile` | who you are: your work, where you live, the languages you use |
| Topics | `topic` | subjects you ask about or follow |
| Areas | `area` | ongoing parts of your life or work: your job, a sport you train for |
| People | `person` | people you mention: "Mia is my sister" |

`--group` takes either column: `--group people` and `--group person` are
the same group.

## See what is remembered

```sh
latere memory list                           # yours, then the projects with memory
latere memory list --project "Tide tables"   # one project's, by its name or id
latere memory show short-lists               # one memory, with its detail
latere memory list --json                    # the platform's answer
```

`list` prints one table, by group, with each memory's id, its text, when it
was last saved and where it came from: `you` for what you added, `import`
for an import, or the id of the conversation that saved it.

```text
GROUP        ID           MEMORY                         UPDATED  FROM
Preferences  short-lists  Keep lists under five things.  2h       you
People       mia          Mia is your sister.            3d       ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX

Projects:
PROJECT      ID                              MEMORIES
Tide tables  prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W  1
`latere memory list --project <name or id>` lists a project's memories.
```

A long text is cut to fit the table; `show` prints it whole, with the
detail kept with it. `--project` takes a project's name, without regard to
case, or its id; a name two of your projects share is refused with both
ids.

## Add and change a memory

```sh
latere memory add "Keep lists under five things."
latere memory add --group people "Mia is my sister. Her birthday is November 22."
latere memory add --project "Tide tables" --group topic "The audience is sailors."
latere memory add --name "Short lists" --body - "Keep lists short." < detail.md

latere memory edit short-lists --text "Keep lists under four things."
latere memory edit mia --group people --body "Lives in Lisbon."
latere memory edit mia --body ""            # remove the detail
```

What you add is saved as you typed it, under Preferences unless `--group`
names another group, and every conversation started from then on knows it. Its name is made from its first words unless `--name` gives
one, and its id from its name. `--body` keeps more detail with it, `-` for
standard input. `edit` changes only what its flags name.

## Move a memory

```sh
latere memory mv short-lists "Tide tables"              # from You to a project
latere memory mv audience you --project "Tide tables"   # from a project to You
```

`--project` names where the memory is now. A move keeps the memory's id
unless the destination has one like it, and prints the id it has now.

## Delete, forget and restore

```sh
latere memory rm short-lists                 # asks first; --yes does not
latere memory forget                         # everything; asks for yes typed in full
latere memory restore                        # what conversations forgot in the last 30 days
latere memory restore trains                 # bring one back
```

A memory you delete with `rm` is gone for good. `forget` deletes every memory
of yours and of every project for good, with what was recently forgotten;
your settings stay as they are. Run `latere memory export` first to keep a
copy.

A conversation forgets a memory when it is no longer true. What a
conversation forgot is kept for 30 days, and `restore` lists it with the
day it is gone for good, and brings one back where it was. A restore is
refused `memory_taken` when a memory with the same id was saved since:
rename or delete that one first.

## Export and import

```sh
latere memory export                         # latere-memory-2026-10-29.zip, here
latere memory export -o ~/backups/memory.zip --force
latere memory import --prompt                # the words to ask another assistant
latere memory import answer.txt              # its answer, a memory per line
pbpaste | latere memory import --project "Tide tables"
```

An export is one zip of Markdown files, as Latere keeps them:
`latere-memory/you/` for your own memory and
`latere-memory/projects/<project name>-<project id>/` for each project,
each folder with its `MEMORY.md` list and one file per memory. The file is
readable only by you. A file that is there already is kept unless
`--force` is given, and an export that stops partway leaves no file
behind.

To bring what another assistant remembers about you, ask it the words
`latere memory import --prompt` prints. It answers with one line per
memory, `Group: what it remembers`. Import that answer from a file, or pipe
it in; at a terminal, `latere memory import` with no file shows the words
and reads what you paste, to Ctrl-D. Each line is saved as it was written,
in its group, with `import` as where it came from. A line you already have
is left out, and the command says how many were. One import reads a
bounded number of lines; the command says when the text had more, so you
import the rest in a second run. Nothing is sent to a model, so an import
costs nothing.

## Settings

```sh
latere memory settings                       # every setting and whether it is on
latere memory settings remember=off
latere memory settings sensitive=off --yes
```

| Setting | What it does |
|---|---|
| `remember` | Remember new things: conversations save new memories. Off, they still use what is remembered and save nothing new. On by default. |
| `sensitive` | Include sensitive topics: memories about health conditions, religious beliefs, political opinions, sexual orientation, racial or ethnic origin, and union membership are kept. Off by default. Turning it off deletes every memory marked sensitive, for good, after you confirm. |

`settings` lists every setting the platform has, including ones added
after your version of `latere`; one it cannot change yet is marked, and
`latere upgrade` installs the release that can.

## When a command is refused

A refusal prints the platform's code and its sentence and exits 1:

| Code | What it means |
|---|---|
| `memory_not_found` | there is no memory with that id where you looked; `latere memory list` shows the ids, with `--project` for a project's |
| `not_found` | you have no project with that id |
| `memory_taken` | a memory with the id you are restoring was saved since |
| `memory_too_large` | the text and the detail together are more than one memory holds; keep the detail short, or keep a long document in your files |
| `invalid_field` | a field breaks its rule, such as a text of more than one line or a group that is none of the five |
| `memory_personal_only` | your current context is an organization's; `latere org --personal` switches back |
| `memory_unavailable`, `storage_unavailable` | memory cannot be reached here, or just now; run the command again in a moment |
| `unauthorized` | the token was refused; run `latere login` again |

## Where the commands go

The commands call the platform at `https://platform.latere.ai`. Each
command presents a token auth mints from your login for the platform's
audience, `api.latere.ai`, valid for five minutes; your login token itself
is never sent.

| Variable | Default | What it does |
|---|---|---|
| `LATERE_PLATFORM_URL` | `https://platform.latere.ai` | The platform's address. `--platform-url` overrides it. |
| `LATERE_PLATFORM_TOKEN` | none | A bearer to present as given instead of minting one. |
