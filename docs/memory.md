# Memory

`latere memory` shows and changes what Latere remembers about you and your
projects. Latere keeps in mind what it learns in your conversations: how you
like to be answered, who you are, the topics and areas of your life you keep
coming back to, and the people you mention. Each memory is one line, in a
group, and says where it came from. The same memories are in Latere Chat's
Settings, and a change made here is there at once. Each night Tidy memory
goes through them, and `latere memory tidies` shows what it changed.

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
for an import, the id of the conversation that saved it, or `tidy` and the
tidy's id for what [Tidy memory](#tidy-memory) wrote.

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

## Tidy memory

Each night Tidy memory goes through what Latere remembers about you, and
each project with something new. It combines memories that say the same
thing, settles ones that contradict each other, and forgets ones that only
recorded a finished task. A night with nothing new starts nothing. Each
night's work on your memory, or on one project's, is one tidy, and you can
see every change it made and undo it.

```sh
latere memory tidies                                  # your tidies, the newest first
latere memory tidies --project "Tide tables"          # one project's
latere memory tidies show ses_01JBWAIT0000000000000000AA          # what one changed
latere memory tidies undo ses_01JBWAIT0000000000000000AA mia      # take back one change
latere memory tidies undo ses_01JBWAIT0000000000000000AA          # take back all of it; asks first
latere memory tidies apply ses_01JBWAIT0000000000000000AA         # apply what waits for you
latere memory tidies decline ses_01JBWAIT0000000000000000AA mia   # decline one change that waits
```

`tidies` lists each tidy with its id, whose memory it went through, when
it ran in your computer's time, where it stands and what it changed:

```text
ID                              IN           TIDIED            STATE          CHANGED
ses_01JBWAIT0000000000000000AA  You          2026-10-30 03:03  Waits for you  2 changes wait for you
ses_01JBTIDE0000000000000000BB  Tide tables  2026-10-29 03:04  Applied        1 added, 2 updated, 1 removed
ses_01JBNONE0000000000000000CC  You          2026-10-28 03:01  Applied        Nothing needed changing
```

| State | What it means |
|---|---|
| Tidying | the tidy is still going; what it changes shows when it is done |
| Waits for you | every change waits for you to apply or decline it |
| Applied | every change was applied, or nothing needed changing |
| Partly applied | some changes were applied and others wait, were declined or were undone |
| Declined | nothing was applied: each change was declined, or left as it was because you changed the memory since |
| Undone | everything the tidy applied was undone |

`show` prints each memory the tidy added, updated or removed, where the
change stands, and the memory before and after: a line marked `-` was
there only before, and one marked `+` only after.

```text
Updated mia: Waits for you
You wrote this memory, so this change waits for your OK.
    Name: Mia
  - Memory: Mia is your sister.
  + Memory: Mia is your sister. She lives in Lisbon.
    Group: People
  - From: you
  + From: Tidy memory, in the tidy ses_01JBWAIT0000000000000000AA
```

`apply`, `decline` and `undo` take the tidy's id, then the memories to
decide by their ids as `show` prints them; without any, they decide every
change they can. Undoing more than one change asks first unless `--yes`
is given. Each says what it did and lists every change as it stands now.

Undo takes back what a tidy applied: a memory it added is removed, one it
updated gets its text back, and one it removed is restored. A declined
change cannot be applied later. A memory you changed after the tidy read
it is left as it is, whichever you choose, and the command says so.

What happens to what Tidy memory changes is the setting `review`:

| `review` | What happens |
|---|---|
| `apply` | Apply, and show me what changed. The default. Changes apply at once, `tidies` lists them, and you can undo them. |
| `ask` | Ask me first. Changes wait until you apply or decline them. Left waiting, they lapse and nothing changes. |

A change to a memory you wrote waits for your OK either way.

## Settings

```sh
latere memory settings                       # every setting and its state
latere memory settings remember=off
latere memory settings chats=off review=ask
latere memory settings sensitive=off --yes
```

| Setting | What it does |
|---|---|
| `remember` | Remember new things: conversations save new memories. Off, they still use what is remembered and save nothing new. On by default. |
| `chats` | Search past chats: conversations search and read your past conversations when you mention one or an answer depends on something you said before. In a project, only that project's conversations. Off, they read none of them. On by default. |
| `sensitive` | Include sensitive topics: memories about health conditions, religious beliefs, political opinions, sexual orientation, racial or ethnic origin, and union membership are kept. Off by default. Turning it off deletes every memory marked sensitive, for good, after you confirm. |
| `review` | When Tidy memory changes something: `apply`, the default, or `ask`. See [Tidy memory](#tidy-memory). |

`settings` lists every on or off setting the platform has, including ones
added after your version of `latere`; one it cannot change yet is marked,
and `latere upgrade` installs the release that can.

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
| `run_not_found` | you have no tidy with that id; `latere memory tidies` lists them |
| `run_running` | the tidy is still going, so what it changes cannot be decided yet |
| `run_not_waiting` | nothing you named waits for you any more, so nothing was decided; `latere memory tidies show <id>` shows where each change stands |
| `run_change_unknown` | the tidy did not change a memory you named |
| `run_busy` | another apply, decline or undo of the tidy is being saved; run the command again in a moment |
| `runs_unavailable` | Tidy memory is not kept here |
| `unauthorized` | the token was refused; run `latere login` again |

An apply or a decline is sent once. When its answer fails for a reason
other than a refusal, the decision may have been saved all the same: the
command says so, and `latere memory tidies show <id>` shows where each
change stands.

## Where the commands go

The commands call the platform at `https://platform.latere.ai`. Each
command presents a token auth mints from your login for the platform's
audience, `api.latere.ai`, valid for five minutes; your login token itself
is never sent.

| Variable | Default | What it does |
|---|---|---|
| `LATERE_PLATFORM_URL` | `https://platform.latere.ai` | The platform's address. `--platform-url` overrides it. |
| `LATERE_PLATFORM_TOKEN` | none | A bearer to present as given instead of minting one. |
