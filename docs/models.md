# Models

`latere models` calls language models through the Latere API at `https://api.latere.ai/v1/models`. You do not create or paste a key: the first time a model command runs, the CLI creates a model key for you and keeps it, and every call presents it. What the key may call and spend follows your current context, your personal account or the organization `latere org` selected.

Run `latere login` first (see the [main README](../README.md#sign-in)).

## List the models

```sh
latere models         # the models your key reaches, one name per line
latere models --json  # the same list as JSON
```

A model is named as the catalog names it, with its provider in front: `anthropic/claude-sonnet-4.6`, `openai/gpt-4.1-mini`. That name is what every other command, and every SDK call, takes.

The list carries names only. Prices per million tokens are in the console's Models section, and what you spent is in its Billing section.

## Point a stock SDK at the Latere API

`models env` prints the exports an SDK reads: the base URL of the door for the SDK's dialect, and your model key as its API key.

```sh
eval "$(latere models env)"                       # OpenAI SDK: OPENAI_BASE_URL, OPENAI_API_KEY
eval "$(latere models env --provider anthropic)"  # Anthropic SDK: ANTHROPIC_BASE_URL, ANTHROPIC_API_KEY
eval "$(latere models env --provider gemini)"     # Gemini SDK: GOOGLE_GEMINI_BASE_URL, GEMINI_API_KEY
```

| `--provider` | Base URL | Key variable |
|---|---|---|
| `openai` (the default) | `https://api.latere.ai/v1/models/openai/v1` | `OPENAI_API_KEY` |
| `anthropic` | `https://api.latere.ai/v1/models/anthropic` | `ANTHROPIC_API_KEY` |
| `gemini` | `https://api.latere.ai/v1/models/gemini` | `GEMINI_API_KEY` |

`--provider` picks the SDK's dialect, not who serves the model. Every door reaches every model in the catalog, and a call to a model whose provider speaks another dialect is translated both ways. So the Anthropic SDK can call `openai/gpt-4.1-mini`, and the OpenAI SDK can call `anthropic/claude-sonnet-4.6`.

The Gemini JavaScript SDK and the Gemini CLI read `GOOGLE_GEMINI_BASE_URL`; the Python SDK takes the base URL in code, through its HTTP options. The Google SDKs prefer `GOOGLE_API_KEY` over `GEMINI_API_KEY`, so unset `GOOGLE_API_KEY` in a shell that uses these exports.

stdout carries the exports alone, so it is safe to `eval`; stderr says which key they carry. Values are shell-quoted where needed, so spaces and shell metacharacters stay literal. `--raw` prints the bare key, for curl and scripts:

```sh
KEY=$(latere models env --raw)
curl https://api.latere.ai/v1/models/openai/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-4.1-mini","messages":[{"role":"user","content":"Hello"}]}'
```

`models env` exits non-zero if it cannot write the exports, the key, or the note on stderr. Check the exit status before using redirected output; a failed write can leave a partial file.

## Check a model with one call

`invoke` sends one prompt through the OpenAI door and prints the reply: no tools, no session. Use it to check that a model answers your key; for assistant work, run an agent instead: `latere topos --local -p "<prompt>"` on this machine, or `latere topos session start <agent-id> -p "<prompt>"` on the hosted platform.

```sh
latere models invoke --model anthropic/claude-sonnet-4.6 "Say hi"
latere models invoke --model openai/gpt-4.1-mini --json "Say hi"   # the whole response
```

An interrupted response, or one larger than 8 MiB, is an error, and nothing is printed. A model your key cannot call says to run `latere models`; a context with nothing left to spend points at the console's Billing section.

## Your model key

The CLI creates the key at auth the first time a model command runs in your current context, allowed to use models and nothing else. It keeps the key in your system keychain, or in `~/.config/latere/model-keys.json` (readable by you alone) on a machine with no keychain, and uses it from then on.

```sh
latere models key          # the key: prefix, context, status, expiry, where it is kept
latere models key revoke   # revoke it; the next model call creates a new one
```

A new key works within a minute, and the model commands wait for it. In an organization that asks its admins to approve member keys, the key works once an org admin approves it. Each key lasts 90 days and is replaced before it ends. A key the Latere API refuses long after it was created, because it was revoked in the console, is replaced on the next call. `latere logout` revokes the keys this login created on this machine.

A CI job can hand the CLI a key with `LATERE_MODEL_KEY`; the CLI then presents that key and needs no login.

## Other commands that call models

`latere review` runs its critics through the Latere API with your model key, and `latere topos --local` does the same once you are signed in. Both take a model named as `latere models` lists it; see [Review](review.md) and [Topos](topos.md).

## Configuration

| Setting | Purpose |
|---------|---------|
| `--models-url` / `LATERE_MODELS_URL` | Override the base URL, `https://api.latere.ai/v1/models`. |
| `--auth-url` / `AUTH_URL` | Override the auth service the key is created and revoked at. |
| `LATERE_MODEL_KEY` | Present this key instead of the one the CLI keeps. |
| `LATERE_MODEL_KEYS_FILE` | Where the model key is kept on a machine with no keychain. |
