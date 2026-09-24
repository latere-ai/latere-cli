# Models (Lux)

`latere lux` calls language models through [Lux](https://lux.latere.ai), the Latere model gateway. You do not allocate or manage an API key: the CLI presents your Latere identity (your `latere login`), and usage is tracked on that identity.

Run `latere login` first (see the [main README](../README.md#sign-in)).

## Discover

```sh
latere lux models      # models visible to you, with rates (USD per million tokens)
```

Each model row names its provider, so `models` is the one discovery
command you need.

## Point a stock SDK at Lux

No key to paste: export your identity and use the SDK normally.

Two independent choices. **Which dialect you speak** is `--compat`, and it reaches any model Lux can route for you:

```sh
eval "$(latere lux env --compat openai)"      # OpenAI SDK, any model
eval "$(latere lux env --compat anthropic)"   # Anthropic SDK, any model
eval "$(latere lux env --compat lux)"         # Latere SDK, the native dialect
# a normal SDK call is now routed through Lux, billed to your identity
```

On a compat surface the provider is not in the route, so name it in the model id when a bare name would be ambiguous. This is how you reach an OpenAI model through the Anthropic SDK:

```sh
eval "$(latere lux env --compat anthropic)"
# then call model "openai/gpt-5"
```

**Which provider serves you** is the positional argument, a passthrough: the provider *is* the route, so only models it serves are reachable, in its own dialect.

```sh
eval "$(latere lux env openai)"       # -> /openai/v1
eval "$(latere lux env anthropic)"    # -> /anthropic
eval "$(latere lux env local)"        # -> /local/v1, your 'lux serve' tunnels
```

The built-in passthroughs are `openai`, `openrouter`, `anthropic`, `moonshot`, `xai`, `zhipu`, and `local`, and a provider Lux lists in your catalog works by name as well, because the CLI reads its route from Lux. Gemini's SDK has no bearer path, so reach Gemini models through a compat surface or the OpenRouter route.

A provider and `--compat` cannot be combined: the exported variables carry a base URL, not a model, so a provider on a compat surface has nowhere to go.

Export values are shell-quoted when needed so spaces and shell metacharacters stay literal. `--raw` prints the token without shell quoting.

`lux env` exits non-zero if it cannot write the exports, raw token, or credential
details. Check the command's exit status before using redirected output; a failed
write can leave a partial file.

The command reports on stderr which credential it embedded and when it expires. The exported value is always a token minted for Lux alone and valid five minutes, never your login token: whatever holds the export reaches Lux and nothing else. Re-run the command when it expires. Missing or empty saved credentials cause an error before any exports are printed; run `latere login` to restore them.

```sh
eval "$(latere lux env --compat openai)"  # a Lux-bound token, five minutes
TOKEN=$(latere lux env --raw)             # bare token for curl/scripts
```

`--token` or `LATERE_LUX_TOKEN` exports that bearer as given instead of minting one.

## Verify access with a raw call

`invoke` sends one raw prompt through the gateway: no tools, no session. Use it to check that a model responds through your identity after binding a provider key; for actual assistant work, run an agent instead: `latere topos --local -p "<prompt>"` on this machine, or `latere topos session start <agent-id> -p "<prompt>"` on the hosted platform.

```sh
latere lux invoke --model openai/gpt-4o-mini "Say hi"
latere lux invoke --provider anthropic --model claude-sonnet-4-6 "Say hi"
```

Interrupted responses and responses larger than 8 MiB return an error without
printing partial output. This applies to both text output and `--json`.

## Your model key on the Latere API

The model endpoints at `https://api.latere.ai/v1/models` take a key, not
your login. The first time `lux env` or `lux invoke` calls
them, the CLI creates a key for you in your current context (your personal
account, or the organization `latere org` selected), allowed to use models
and nothing else. It keeps the key in your system keychain, or in
`~/.config/latere/model-keys.json` (readable by you alone) on a machine with
no keychain, and uses it from then on.

```sh
export LUX_API_URL=https://api.latere.ai/v1/models
eval "$(latere lux env --compat openai)"   # creates the key on first use
latere lux key                             # the key: prefix, context, status, expiry
latere lux key revoke                      # revoke it; the next call creates a new one
```

A new key works within a minute; `lux invoke` waits for it. What the key
may call and spend follows its context: your models and wallet, or the
organization's. In an organization that asks its admins to approve member
keys, the key works once an org admin approves it. Each key lasts 90 days
and is replaced before it ends. `latere logout` revokes the keys this login
created on this machine. A CI job can hand the CLI a key with
`LATERE_MODEL_KEY`.

## Usage and access

```sh
latere lux usage                  # last 30 days: total, per-model breakdown, cost chart
latere lux usage --period week    # day, week, month, quarter, or year
latere lux usage --by provider    # break down by provider instead of model
latere lux access show
```

A model resolves through a provider key you or your org own (bind it with `latere lux access set --model <m> --provider <p> --provider-key <id>`), or through a **platform grant** a Latere admin configured for you (optionally capped per month). Granted models just show up in `latere lux models`; no binding needed. Past a grant's cap, calls return HTTP 402 until the month rolls over.

## Serve a local model

Expose a model running on your own machine (Ollama, vLLM, LM Studio, llama.cpp, Apple MLX) through Lux, so it is callable from anywhere as `local/<model>` with your identity:

```sh
latere lux serve                    # Ollama at localhost:11434 (default)
latere lux serve --runtime vllm     # or lmstudio / llamacpp / mlx
latere lux serve --upstream http://localhost:1234 --models llama3.1:8b
latere lux serve --share org        # share with your whole org (default for org accounts)
```

`serve` opens a long-lived outbound tunnel (no inbound port) and forwards requests only to the configured local runtime. It needs no special scope: any signed-in identity can serve, except a virtual key, which cannot open a tunnel. Run `latere login` if you are not signed in. Call the model by pointing a stock SDK at the `/local/v1` route: `eval "$(latere lux env local)"`, or call it directly with `latere lux invoke --model local/<model> "Say hi"` (the bare `<model>` works too).

## Configuration

| Setting | Purpose |
|---------|---------|
| `--lux-url` / `LUX_API_URL` | Override the Lux base URL for `latere lux`. |
| `LATERE_LUX_TOKEN` | Present this bearer to Lux instead of your login (e.g. a service token). |
| `LATERE_MODEL_KEY` | Present this key to the model endpoints instead of the one the CLI keeps. |
| `LATERE_MODEL_KEYS_FILE` | Where the model key is kept on a machine with no keychain. |
