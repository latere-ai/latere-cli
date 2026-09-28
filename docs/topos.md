# Topos

`latere topos --local` runs a Topos coding agent on this machine. The agent
works in your directory on your real files, like Claude Code. No control
plane and no Latere sign-in are required.

The hosted Topos service is retired, and its sessions were not migrated.
`latere topos` without `--local` stops with an error that says so.

## Running the agent

```sh
latere topos --local                               # an interactive session in the current directory
latere topos --local --dir ~/code/project          # in another directory
latere topos --local -p "add a test for foo()"     # run one prompt, stream the result, and exit
latere topos --local --model anthropic/claude-sonnet-4.6  # pick the model
```

The agent reads, edits, and runs commands directly in the working
directory, as you, with no isolation and no approval prompt. Run it where
you would run a coding assistant of your own, not in a directory you do
not want changed.

Inside the interactive session, `/model <name>` switches the model,
`/model` alone lists the models your model key reaches, `/help` lists the
commands, and `/quit` leaves.

### Choosing the model

`--local` uses the first of these that is available:

1. `ANTHROPIC_API_KEY`, calling Anthropic directly.
2. The provider you chose with `latere topos login`.
3. The Latere API, when you are signed in with `latere login` or hand the
   CLI a key with `LATERE_MODEL_KEY`: the call presents your
   [model key](models.md#your-model-key) and is billed to its context,
   with no provider key on your machine. This is the default once you are
   signed in. Name a model as `latere models` lists it; the default is
   `anthropic/claude-sonnet-4.6`.
4. `CLAUDE_CODE_OAUTH_TOKEN`, the token Claude Code uses, which shares
   Claude Code's rate limit.

With none of them, an interactive session opens the provider picker, and
`-p` or a run without a terminal stops with an error that names what to
set.

```sh
latere topos login
```

opens the same picker: sign in with Claude in your browser, paste an
Anthropic API key, or use Ollama for models running on this machine. The
choice is saved in your user configuration directory and wins over
`CLAUDE_CODE_OAUTH_TOKEN`, so an API key or Ollama keeps the local agent
off Claude Code's shared rate limit.

## Settings

| Setting | Purpose |
|---------|---------|
| `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` | Model credentials for `--local`, in the order above. |

[configuration.md](configuration.md) lists the files `latere topos login`
keeps.
