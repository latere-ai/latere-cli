# Models (Lux)

`latere lux` calls language models through [Lux](https://lux.latere.ai), the Latere model gateway. You do not allocate or manage an API key: the CLI presents your Latere identity (your `latere auth login`), and usage is tracked on that identity. Inside a Cella sandbox that is allowed to reach Lux, the sandbox's own token is used automatically.

Run `latere auth login` first (see the [main README](../README.md#sign-in)).

## Discover

```sh
latere lux models
latere lux providers
latere lux rates
```

## Point a stock SDK at Lux

No key to paste — export your identity and use the SDK normally:

```sh
eval "$(latere lux env --provider openai)"
# a normal OpenAI SDK call is now routed through Lux, billed to your identity
```

`--provider` accepts `openai`, `openrouter`, or `anthropic`. The exported token lasts your sign-in session; re-run the command when it expires.

## One-shot chat

```sh
latere lux chat --model openai/gpt-4o-mini "Say hi"
latere lux chat --provider anthropic --model claude-sonnet-4-6 "Say hi"
```

## Usage and access

```sh
latere lux usage
latere lux access show
```

Free models work with no setup. A paid model needs your access profile bound to a provider key — `latere lux access set --model <m> --provider <p> --provider-key <id>` (or ask your Latere admin to enable one for you).

## Configuration

| Setting | Purpose |
|---------|---------|
| `--lux-url` / `LUX_API_URL` | Override the Lux base URL for `latere lux`. |
| `LATERE_LUX_TOKEN` | Present this bearer to Lux instead of your login (e.g. a service token). |
