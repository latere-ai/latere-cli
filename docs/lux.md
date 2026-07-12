# Models (Lux)

`latere lux` calls language models through [Lux](https://lux.latere.ai), the Latere model gateway. You do not allocate or manage an API key: the CLI presents your Latere identity (your `latere login`), and usage is tracked on that identity. Inside a Cella sandbox that is allowed to reach Lux, the sandbox's own token is used automatically.

Run `latere login` first (see the [main README](../README.md#sign-in)).

## Discover

```sh
latere lux models      # models visible to you, with rates (USD per million tokens)
latere lux providers
```

## Point a stock SDK at Lux

No key to paste — export your identity and use the SDK normally:

```sh
eval "$(latere lux env --provider openai)"
# a normal OpenAI SDK call is now routed through Lux, billed to your identity
```

`--provider` accepts `openai`, `openrouter`, or `anthropic`. The exported token lasts your sign-in session; re-run the command when it expires.

## Verify access with a raw call

`invoke` sends one raw prompt through the gateway — no tools, no session. Use it to check that a model responds through your identity after binding a provider key; for actual assistant work, use `latere topos -p "<prompt>"`.

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

Free models work with no setup. A paid model needs your access profile bound to a provider key — `latere lux access set --model <m> --provider <p> --provider-key <id>` (or ask your Latere admin to enable one for you).

## Serve a local model

Expose a model running on your own machine (Ollama, vLLM, LM Studio, llama.cpp, Apple MLX) through Lux, so it is callable from anywhere as `local/<model>` with your identity:

```sh
latere lux serve                    # Ollama at localhost:11434 (default)
latere lux serve --runtime vllm     # or lmstudio / llamacpp / mlx
latere lux serve --upstream http://localhost:1234 --models llama3.1:8b
latere lux serve --share org        # share with your whole org (default for org accounts)
```

`serve` opens a long-lived outbound tunnel (no inbound port) and forwards requests only to the configured local runtime. It needs the `llm.serve` scope — run `latere login` once to refresh your scopes. Call the model by pointing a stock SDK at the `/local/v1` route: `eval "$(latere lux env --provider local)"`. See the [Lux local-models guide](https://github.com/latere-ai/lux/blob/main/docs/lux/local-models.md).

## Configuration

| Setting | Purpose |
|---------|---------|
| `--lux-url` / `LUX_API_URL` | Override the Lux base URL for `latere lux`. |
| `LATERE_LUX_TOKEN` | Present this bearer to Lux instead of your login (e.g. a service token). |
