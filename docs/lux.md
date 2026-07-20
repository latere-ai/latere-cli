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

Run `latere lux providers` for the current list. The two cannot be combined: env vars carry a base URL, not a model, so a provider on a compat surface has nowhere to go.

The command reports on stderr which credential it embedded and when it expires: by default your login identity token, which lasts the sign-in session.

```sh
eval "$(latere lux env --compat openai --ttl 1h)"  # CI: a short-lived actor token
TOKEN=$(latere lux env --raw)                      # bare token for curl/scripts
```

## Verify access with a raw call

`invoke` sends one raw prompt through the gateway: no tools, no session. Use it to check that a model responds through your identity after binding a provider key; for actual assistant work, use `latere topos -p "<prompt>"`.

```sh
latere lux invoke --model openai/gpt-4o-mini "Say hi"
latere lux invoke --provider anthropic --model claude-sonnet-4-6 "Say hi"
```

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

`serve` opens a long-lived outbound tunnel (no inbound port) and forwards requests only to the configured local runtime. It needs no special scope: any signed-in identity can serve, except a virtual key, which cannot open a tunnel. Run `latere login` if you are not signed in. Call the model by pointing a stock SDK at the `/local/v1` route: `eval "$(latere lux env local)"`, or call it directly with `latere lux invoke --model local/<model> "Say hi"` (the bare `<model>` works too). See the [Lux local-models guide](https://github.com/latere-ai/lux/blob/main/docs/lux/local-models.md).

## Configuration

| Setting | Purpose |
|---------|---------|
| `--lux-url` / `LUX_API_URL` | Override the Lux base URL for `latere lux`. |
| `LATERE_LUX_TOKEN` | Present this bearer to Lux instead of your login (e.g. a service token). |
