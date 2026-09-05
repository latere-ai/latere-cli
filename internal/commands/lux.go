// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"latere.ai/x/pkg/otel"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/tunnel"
)

// Lux is the Latere model gateway at lux.latere.ai. These commands let
// the CLI call models with the user's identity instead of an allocated
// key: cost is tracked on the identity.

// ---- provider surface ----

// providerSpec maps a CLI provider name to its Lux proxy paths and the
// environment knobs a stock SDK needs to point at Lux. Lux authenticates
// the caller via Authorization: Bearer only and strips x-api-key /
// x-goog-api-key before forwarding, so SDK enablement works for SDKs
// that send the credential as a bearer:
//   - OpenAI SDK: OPENAI_API_KEY -> Authorization: Bearer (works as-is)
//   - Anthropic SDK: ANTHROPIC_AUTH_TOKEN -> Authorization: Bearer
//     (NOT ANTHROPIC_API_KEY, which sends x-api-key and Lux ignores)
//   - Gemini SDK: sends x-goog-api-key only — no bearer path, so `env`
//     is not offered; use `lux invoke` or an OpenRouter route instead.
type providerSpec struct {
	name string
	// chatPath is the full Lux path for `lux invoke`; empty = chat
	// unsupported for this provider.
	chatPath string
	// anthropicStyle selects the Messages request/response shape and
	// the anthropic-version header (vs OpenAI chat/completions).
	anthropicStyle bool
	// env enablement; envBaseVar == "" means `lux env` is unsupported.
	envBaseVar string
	envKeyVar  string
	envBaseURL string // suffix joined onto the Lux base URL
}

func providerSpecs() map[string]providerSpec {
	return map[string]providerSpec{
		"openai": {
			name: "openai", chatPath: "/openai/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/openai/v1",
		},
		"openrouter": {
			// OpenAI-compatible: use the OpenAI SDK pointed at the
			// OpenRouter route.
			name: "openrouter", chatPath: "/openrouter/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/openrouter/v1",
		},
		"anthropic": {
			name: "anthropic", chatPath: "/anthropic/v1/messages", anthropicStyle: true,
			// The Anthropic SDK appends /v1/messages, so the base is the
			// bare /anthropic prefix. ANTHROPIC_AUTH_TOKEN -> bearer.
			envBaseVar: "ANTHROPIC_BASE_URL", envKeyVar: "ANTHROPIC_AUTH_TOKEN", envBaseURL: "/anthropic",
		},
		"moonshot": {
			// OpenAI-compatible (Kimi families).
			name: "moonshot", chatPath: "/moonshot/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/moonshot/v1",
		},
		"xai": {
			// OpenAI-compatible (Grok families).
			name: "xai", chatPath: "/xai/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/xai/v1",
		},
		"zhipu": {
			// OpenAI-compatible (GLM families). The /v1 here is the Lux
			// route, not Zhipu's: upstream serves /api/paas/v4, and the
			// gateway rewrites the path, so callers address it like any
			// other provider.
			name: "zhipu", chatPath: "/zhipu/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/zhipu/v1",
		},
		"gemini": {
			// Reachable via `lux invoke`-style direct call is non-trivial
			// (generateContent), and the Gemini SDK has no bearer path,
			// so neither chat nor env is offered here.
			name: "gemini",
		},
		"local": {
			// Local runtimes tunneled in via `lux serve`. They speak the
			// openai-compat dialect, so the OpenAI SDK pointed at the
			// /local/v1 route just works. No upstream key.
			name: "local", chatPath: "/local/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/local/v1",
		},
	}
}

// compatDialect names a wire dialect a caller can speak, independently
// of which provider ends up serving the call.
type compatDialect string

const (
	// compatPassthrough pins the call to one provider's own surface. The
	// provider is the route, so only models that provider serves are
	// reachable.
	compatPassthrough compatDialect = "passthrough"
	compatOpenAI      compatDialect = "openai"
	compatAnthropic   compatDialect = "anthropic"
	// compatLux is the first-party Lux dialect. luxsdk and its TypeScript
	// and Python siblings append /lux/v1/generate themselves, so the base
	// carries no suffix.
	compatLux compatDialect = "lux"
)

func compatDialects() []compatDialect {
	return []compatDialect{compatPassthrough, compatOpenAI, compatAnthropic, compatLux}
}

// compatSpec is the env shape for a non-passthrough dialect. These
// surfaces reach any model Lux routes rather than one provider's, so
// they carry no provider in the path: a caller names the provider in
// the model id ("openai/gpt-5") when a bare name would be ambiguous.
func compatSpec(d compatDialect) (providerSpec, error) {
	switch d {
	case compatOpenAI:
		return providerSpec{
			name:       string(d),
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/compat/openai/v1",
		}, nil
	case compatAnthropic:
		// The Anthropic SDK appends /v1/messages, so the base stops at
		// the compat prefix.
		return providerSpec{
			name: string(d), anthropicStyle: true,
			envBaseVar: "ANTHROPIC_BASE_URL", envKeyVar: "ANTHROPIC_AUTH_TOKEN", envBaseURL: "/compat/anthropic",
		}, nil
	case compatLux:
		return providerSpec{
			name:       string(d),
			envBaseVar: "LUX_BASE_URL", envKeyVar: "LUX_API_KEY", envBaseURL: "",
		}, nil
	default:
		return providerSpec{}, fmt.Errorf("unknown --compat %q; one of: %s",
			d, strings.Join(compatDialectNames(), ", "))
	}
}

func compatDialectNames() []string {
	out := make([]string, 0, len(compatDialects()))
	for _, d := range compatDialects() {
		out = append(out, string(d))
	}
	return out
}

// localProviderName is the route serving `lux serve` tunnels. Its catalog
// rows are listed under the prefixed id `local/<model>` while the
// /local/v1 surface resolves the bare runtime id, so the prefix is
// stripped on the wire (see localWireModel).
const localProviderName = "local"

// localWireModel returns the model id to put on the wire for a route. The
// local surface matches tunnels by their bare runtime id, so a `local/`
// prefix (the id shown by `lux models`, hence the id users type) is
// dropped. Every other route is passed through untouched: openrouter ids
// are legitimately slash-qualified.
func localWireModel(provider, model string) string {
	// The gateway addresses models as `<provider>/<model>` on its compat and
	// lux-native surfaces, so users reasonably type that here too. Strip the
	// prefix when it names the provider we resolved: the passthrough route
	// already encodes the provider in the path and wants the bare upstream
	// id. Non-matching prefixes are left alone, so OpenRouter ids that
	// genuinely contain a slash (anthropic/claude-...) survive.
	if provider != "" && strings.HasPrefix(model, provider+"/") {
		return strings.TrimPrefix(model, provider+"/")
	}
	return model
}

// deriveProviderSpec builds a spec from what Lux publishes about a provider:
// its route prefix and the wire dialect that route speaks. Everything the CLI
// needs (chat path, request shape, SDK env vars) follows from those two, so a
// provider Lux adds is usable here with no CLI release.
func deriveProviderSpec(id, dialect, prefix string) providerSpec {
	if prefix == "" {
		prefix = "/" + id
	}
	switch dialect {
	case "anthropic-messages":
		// The Anthropic SDK appends /v1/messages, so its base is the bare
		// prefix. ANTHROPIC_AUTH_TOKEN -> bearer (ANTHROPIC_API_KEY would
		// send x-api-key, which Lux ignores).
		return providerSpec{
			name: id, chatPath: prefix + "/v1/messages", anthropicStyle: true,
			envBaseVar: "ANTHROPIC_BASE_URL", envKeyVar: "ANTHROPIC_AUTH_TOKEN", envBaseURL: prefix,
		}
	case "gemini":
		// Gemini speaks generateContent and its SDK has no bearer path, so
		// neither direct chat nor env enablement is offered.
		return providerSpec{name: id}
	default: // openai-chat, the majority shape
		return providerSpec{
			name: id, chatPath: prefix + "/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: prefix + "/v1",
		}
	}
}

// resolveProviderSpecs asks Lux which providers it serves and derives a spec
// for each. The built-in table is the fallback when the catalog is
// unreachable (offline, or an older server that predates the dialect field),
// and it also supplies `local`, which is a CLI-side pseudo-provider that no
// server lists.
func resolveProviderSpecs(ctx context.Context, luxURL, authURL, token string) map[string]providerSpec {
	specs := providerSpecs()
	c, _, err := luxClient(ctx, luxURL, authURL, token)
	if err != nil {
		return specs
	}
	var resp luxCatalogResponse
	if err := c.GetJSON(ctx, "/lux/v1/providers", &resp); err != nil {
		return specs
	}
	for _, it := range resp.Items {
		id, _ := it["id"].(string)
		if id == "" {
			continue
		}
		dialect, _ := it["dialect"].(string)
		prefix, _ := it["default_route_prefix"].(string)
		if dialect == "" {
			// Older server: keep whatever the built-in table says rather
			// than guessing a dialect for it.
			if _, ok := specs[id]; ok {
				continue
			}
			dialect = "openai-chat"
		}
		specs[id] = deriveProviderSpec(id, dialect, prefix)
	}
	return specs
}

// lookupProviderFor resolves a provider against the server's live list,
// falling back to the built-in table. This is what keeps `lux chat` working
// for a provider added to Lux after this binary shipped.
func lookupProviderFor(ctx context.Context, luxURL, authURL, token, name string) (providerSpec, error) {
	// Fast path: a provider this binary already knows needs no round trip,
	// so the common case adds no latency and still works offline. Only an
	// unrecognised provider — one Lux gained after this binary shipped —
	// costs a catalog fetch.
	if p, ok := providerSpecs()[name]; ok {
		return p, nil
	}
	specs := resolveProviderSpecs(ctx, luxURL, authURL, token)
	p, ok := specs[name]
	if !ok {
		return providerSpec{}, fmt.Errorf("unknown provider %q; one of: %s",
			name, strings.Join(slices.Sorted(maps.Keys(specs)), ", "))
	}
	return p, nil
}

// ---- top-level ----

func newLuxCmd() *cobra.Command {
	var luxURL, authURL, token string
	cmd := &cobra.Command{
		Use:   "lux",
		Short: "Call models through Latere Lux with your identity (no key allocation).",
		Long: `Call models through Latere Lux (lux.latere.ai) using your identity.

Lux is the Latere model gateway. Instead of allocating an API key, the
CLI presents your identity (your user login) and Lux tracks cost on that
identity.

Run 'latere login' first to sign in. The base URL defaults to
https://lux.latere.ai and can be overridden by LUX_API_URL or --lux-url.

A model resolves through a provider key you or your org own (see
'latere lux access'), or through a platform grant a Latere admin
configured for you — granted models simply appear in 'latere lux
models'.`,
		Example: `  latere lux models
  eval "$(latere lux env --provider openai)"
  latere lux invoke --model openai/gpt-4o-mini "Say hi"
  latere lux usage`,
	}
	cmd.PersistentFlags().StringVar(&luxURL, "lux-url", "", "override Lux base URL (overrides LUX_API_URL)")
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override auth base URL (default $AUTH_URL or derived from the Lux URL)")
	cmd.PersistentFlags().StringVar(&token, "token", "", "present this bearer to Lux instead of minting one (e.g. a service token)")

	cmd.AddCommand(newLuxModelsCmd(&luxURL, &authURL, &token))
	// Deprecated: rates ride `lux models` now, and every model row names
	// its provider, so the static provider list adds nothing. Both stay
	// hidden but functional so scripts keep working.
	rates := newLuxCatalogCmd("rates", "/lux/v1/rates", "model rate card", &luxURL, &authURL, &token)
	rates.Hidden = true
	cmd.AddCommand(rates)
	providers := newLuxCatalogCmd("providers", "/lux/v1/providers", "providers Lux can route to", &luxURL, &authURL, &token)
	providers.Hidden = true
	cmd.AddCommand(providers)
	cmd.AddCommand(newLuxEnvCmd(&luxURL, &authURL, &token))
	cmd.AddCommand(newLuxTokenCmd(&luxURL, &authURL, &token))
	cmd.AddCommand(newLuxChatCmd(&luxURL, &authURL, &token))
	cmd.AddCommand(newLuxUsageCmd(&luxURL, &authURL, &token))
	cmd.AddCommand(newLuxAccessCmd(&luxURL, &authURL, &token))
	cmd.AddCommand(newLuxServeCmd(&luxURL, &authURL, &token))
	return cmd
}

// ---- serve: expose a local model runtime through Lux ----

func newLuxServeCmd(luxURL, authURL, token *string) *cobra.Command {
	var runtime, upstream, models, share string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Expose a local model runtime (Ollama, vLLM, LM Studio, llama.cpp, MLX) through Lux.",
		Long: `Open a reverse tunnel from this machine to Lux so your local models are
callable through lux.latere.ai from anywhere, with your identity and the
same gates and request log as any other Lux model.

This runs a long-lived outbound connection (no inbound port is opened) and
forwards inbound requests only to the configured local runtime. Run
'latere login' first to sign in.

Discoverable as local/<model> in 'latere lux models'. Call it by pointing
an OpenAI-compatible SDK at <lux>/local/v1.`,
		Example: `  latere lux serve
  latere lux serve --runtime vllm
  latere lux serve --upstream http://localhost:1234 --models llama3.1:8b
  latere lux serve --share org`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Availability first: a missing local runtime is the most
			// common failure and deserves a plain answer, not a tunnel
			// reconnect loop of dial errors.
			upstreamBase := upstream
			if upstreamBase == "" {
				upstreamBase = tunnel.DefaultURL(runtime)
			}
			found, err := tunnel.Preflight(cmd.Context(), runtime, upstream, splitCSV(models))
			if err != nil {
				return fmt.Errorf("no %s runtime is answering at %s.\n%s\nOr serve a different runtime: --runtime ollama|vllm|lmstudio|llamacpp|mlx, or --upstream http://localhost:<port>", runtime, upstreamBase, localRuntimeStartHint(runtime))
			}
			fmt.Fprintf(os.Stderr, "Local %s runtime at %s: %d model(s).\n", runtime, upstreamBase, len(found))

			bearerFn := func(ctx context.Context) (string, error) {
				return luxIdentityBearer(ctx, *token, *luxURL, *authURL)
			}
			// Resolve sharing scope: explicit flag wins; otherwise default
			// to org when the identity carries an org claim (so connecting
			// in an org exposes the models org-wide), else owner-private.
			resolvedShare := share
			if resolvedShare == "" || resolvedShare == "auto" {
				if b, err := bearerFn(cmd.Context()); err == nil && bearerHasOrg(b) {
					resolvedShare = "org"
				} else {
					resolvedShare = "owner"
				}
			}
			scopeMsg := "private to you"
			if resolvedShare == "org" {
				scopeMsg = "shared with your whole org"
			}
			fmt.Fprintf(os.Stderr, "Sharing scope: %s.\n", scopeMsg)

			return tunnel.Run(cmd.Context(), tunnel.Options{
				LuxURL:      resolveLuxURL(*luxURL),
				Bearer:      bearerFn,
				Runtime:     runtime,
				UpstreamURL: upstream,
				Models:      splitCSV(models),
				Share:       resolvedShare,
				NodeID:      tunnel.NodeID(),
				Out:         os.Stderr,
			})
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "ollama", "local runtime: ollama, vllm, lmstudio, llamacpp, mlx, or openai-compat")
	cmd.Flags().StringVar(&upstream, "upstream", "", "local runtime base URL (default per --runtime)")
	cmd.Flags().StringVar(&models, "models", "", "comma-separated allowlist (default: all discovered models)")
	cmd.Flags().StringVar(&share, "share", "auto", "who may call: owner, org, or auto (org if your identity has one)")
	return cmd
}

// localRuntimeStartHint says how to start the runtime `lux serve`
// could not reach — the actionable half of the availability message.
func localRuntimeStartHint(rt string) string {
	switch rt {
	case "ollama":
		return "Start it with `ollama serve` (or open the Ollama app)."
	case "lmstudio":
		return "Start the LM Studio local server (Developer tab → Start Server)."
	case "mlx":
		return "Start it with `mlx_lm.server`."
	case "vllm":
		return "Start it with `vllm serve <model>`."
	case "llamacpp":
		return "Start it with `llama-server -m <model.gguf>`."
	default:
		return "Start your OpenAI-compatible server."
	}
}

// bearerHasOrg reports whether a JWT bearer carries a non-empty org_id
// claim. A non-JWT (opaque) token returns false.
func bearerHasOrg(bearer string) bool {
	return strings.TrimSpace(stringClaim(decodeJWTClaims(bearer), "org_id")) != ""
}

// splitCSV splits a comma-separated list, trimming spaces and dropping
// empties.
func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ---- discovery: models / providers / rates ----

// luxCatalogResponse is the shared {"items":[...]} envelope returned by
// Lux's catalog endpoints. Items are kept as raw maps so additive
// backend fields render without DTO churn.
type luxCatalogResponse struct {
	Items []map[string]any `json:"items"`
}

// newLuxModelsCmd lists the models visible to the caller with the rate
// card joined in: a price you can't see next to the model name is not a
// catalog. Rates are best-effort — the model list still prints if the
// rate endpoint is unavailable.
func newLuxModelsCmd(luxURL, authURL, token *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List models visible to your identity, with their rates.",
		Long: `List models visible to your identity, including the rate card
(USD per million input/output/cached-input tokens) where one applies.

Reads /lux/v1/models and /lux/v1/rates with your identity.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			var models luxCatalogResponse
			if err := c.GetJSON(cmd.Context(), "/lux/v1/models", &models); err != nil {
				return wrapLuxErr(err)
			}
			var rates luxCatalogResponse
			if err := c.GetJSON(cmd.Context(), "/lux/v1/rates", &rates); err != nil {
				fprintf(cmd.ErrOrStderr(), "warning: rate card unavailable (%v)\n", err)
			}
			for _, m := range models.Items {
				provider, _ := m["provider"].(string)
				name, _ := m["model"].(string)
				r := rateFor(rates.Items, provider, name)
				if r == nil {
					continue
				}
				for _, k := range []string{"input_usd_per_m", "output_usd_per_m", "input_cached_usd_per_m"} {
					if v, ok := r[k]; ok {
						m[k] = v
					}
				}
			}
			if jsonF {
				return printJSON(models.Items)
			}
			if len(models.Items) == 0 {
				fmt.Println("No models.")
				return nil
			}
			// Human output: fold the three numbers into one rate line.
			for _, m := range models.Items {
				if line := rateLine(m); line != "" {
					m["rate"] = line
					delete(m, "input_usd_per_m")
					delete(m, "output_usd_per_m")
					delete(m, "input_cached_usd_per_m")
				}
			}
			printLuxItems(models.Items)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// rateLine folds a model's joined rate fields into one readable line,
// e.g. "$2.5/M in, $15/M out ($0.25/M cached input)".
func rateLine(m map[string]any) string {
	in, okIn := m["input_usd_per_m"].(float64)
	out, okOut := m["output_usd_per_m"].(float64)
	if !okIn || !okOut {
		return ""
	}
	s := fmt.Sprintf("$%s/M in, $%s/M out", fmtRate(in), fmtRate(out))
	if c, ok := m["input_cached_usd_per_m"].(float64); ok {
		s += fmt.Sprintf(" ($%s/M cached input)", fmtRate(c))
	}
	return s
}

// fmtRate renders a USD-per-million price compactly, rounding away
// binary float artifacts from the wire (0.024999999999999998 → 0.025).
func fmtRate(v float64) string {
	return strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64)
}

// rateFor picks the rate-card entry for provider/model: an exact model
// match wins; otherwise the longest trailing-* prefix pattern (the rate
// card keys entries like "gpt-5.6-terra*").
func rateFor(rates []map[string]any, provider, model string) map[string]any {
	var best map[string]any
	bestLen := -1
	for _, r := range rates {
		p, _ := r["provider"].(string)
		if p != provider {
			continue
		}
		pat, _ := r["model"].(string)
		if pat == model {
			return r
		}
		if strings.HasSuffix(pat, "*") && strings.HasPrefix(model, strings.TrimSuffix(pat, "*")) && len(pat) > bestLen {
			best, bestLen = r, len(pat)
		}
	}
	return best
}

func newLuxCatalogCmd(name, path, what string, luxURL, authURL, token *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   name,
		Short: "List " + what + ".",
		Long:  fmt.Sprintf("List %s.\n\nReads %s with your identity.", what, path),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			var resp luxCatalogResponse
			if err := c.GetJSON(cmd.Context(), path, &resp); err != nil {
				return wrapLuxErr(err)
			}
			if jsonF {
				return printJSON(resp.Items)
			}
			if len(resp.Items) == 0 {
				fmt.Printf("No %s.\n", what)
				return nil
			}
			printLuxItems(resp.Items)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// printLuxItems renders catalog rows compactly: id/model headline plus a
// few well-known fields, falling back to whatever keys are present.
func printLuxItems(items []map[string]any) {
	for i, it := range items {
		if i > 0 {
			fprintln(os.Stdout)
		}
		for _, k := range []string{"id", "model", "provider", "name", "status", "default_route_prefix",
			"rate", "input_usd_per_m", "output_usd_per_m"} {
			if v, ok := it[k]; ok && v != nil {
				printWrappedField(k, fmt.Sprintf("%v", v))
			}
		}
	}
}

// ---- SDK enablement: env / token ----

// luxEnvBearer resolves the credential lux env embeds, and says what it
// is: a passthrough token (flag/env), a minted actor token
// bounded by ttl, or the refreshed login identity token with its expiry.
func luxEnvBearer(ctx context.Context, tokenFlag, luxURL, authURLFlag string, ttl time.Duration) (bearer, provenance string, err error) {
	if t, ok := passthroughToken(tokenFlag); ok {
		return t, "passthrough token (--token or $LATERE_LUX_TOKEN)", nil
	}
	access, authBase, err := authIdentityToken(ctx, luxURL, authURLFlag)
	if err != nil {
		return "", "", err
	}
	if ttl > 0 {
		httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil)}
		actor, expiresIn, err := api.MintActorTokenWithLifetime(ctx, httpc, authBase, access, "lux.latere.ai", int(ttl/time.Second))
		if err != nil {
			return "", "", fmt.Errorf("mint actor token: %w", err)
		}
		if expiresIn > 0 {
			unit := "seconds"
			if expiresIn == 1 {
				unit = "second"
			}
			return actor, fmt.Sprintf("actor token, expires in %d %s", expiresIn, unit), nil
		}
		return actor, "actor token, expiry not reported by auth", nil
	}
	provenance = "identity token"
	if tok, lerr := api.LoadAuthToken(); lerr == nil && !tok.ExpiresAt.IsZero() {
		provenance = fmt.Sprintf("identity token, expires %s — re-run after expiry",
			tok.ExpiresAt.UTC().Format("2006-01-02T15:04Z"))
	}
	return access, provenance, nil
}

// resolveEnvSurface applies the two axes: a [provider] argument selects
// that provider's passthrough route, --compat selects a dialect surface
// that carries no provider at all.
//
// Neither one is defaulted. `lux env` used to mean the OpenAI
// passthrough, which reads as "the default way to reach Lux" when it is
// really one vendor's route out of several, and silently pins a caller
// to OpenAI's models. Requiring the choice costs one word and removes
// the guess.
func resolveEnvSurface(
	ctx context.Context,
	luxURL, authURL, token, provider string,
	compat compatDialect,
	raw bool,
) (providerSpec, error) {
	// --raw prints a bearer and nothing else, so no surface is involved.
	if raw {
		return providerSpec{}, nil
	}
	switch {
	case provider != "" && compat != "" && compat != compatPassthrough:
		return providerSpec{}, fmt.Errorf(
			"cannot combine provider %q with --compat %s: a compat surface carries no provider in its "+
				"route, so name it in the model id instead (e.g. %q). Drop the provider argument",
			provider, compat, provider+"/MODEL")
	case provider == "" && (compat == "" || compat == compatPassthrough):
		return providerSpec{}, fmt.Errorf(
			"specify a passthrough provider or a compat dialect\n\n"+
				"  latere lux env openai              # OpenAI passthrough\n"+
				"  latere lux env --compat openai     # OpenAI dialect, any model\n"+
				"  latere lux env --compat anthropic  # Anthropic dialect, any model\n"+
				"  latere lux env --compat lux        # native dialect, any model\n\n"+
				"providers: %s",
			strings.Join(slices.Sorted(maps.Keys(resolveProviderSpecs(ctx, luxURL, authURL, token))), ", "))
	case compat != "" && compat != compatPassthrough:
		return compatSpec(compat)
	}

	spec, err := lookupProviderFor(ctx, luxURL, authURL, token, provider)
	if err != nil {
		return providerSpec{}, err
	}
	if spec.envBaseVar == "" {
		return providerSpec{}, fmt.Errorf(
			"`lux env` does not support %q (its SDK has no bearer path); use `lux invoke` or a --compat dialect",
			provider)
	}
	return spec, nil
}

func newLuxEnvCmd(luxURL, authURL, token *string) *cobra.Command {
	var provider string
	var compat string
	var ttl time.Duration
	var raw bool
	cmd := &cobra.Command{
		Use: "env [provider] [--compat DIALECT]",
		// Completion is served from the live provider list, not a literal,
		// so a provider Lux adds is suggested without a CLI release.
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			specs := resolveProviderSpecs(cmd.Context(), *luxURL, *authURL, *token)
			out := make([]string, 0, len(specs))
			for _, name := range slices.Sorted(maps.Keys(specs)) {
				if specs[name].envBaseVar != "" {
					out = append(out, name)
				}
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		Short: "Print shell exports that point a stock SDK at a Lux route (keyless).",
		Long: `Print 'export' lines that point a stock SDK at a Lux surface using
your identity as the credential — no key allocation.

Two independent choices: who serves the call, and which dialect you
speak to it in.

    latere lux env openai               # OpenAI passthrough, /openai/v1
    latere lux env --compat openai      # OpenAI dialect, any model Lux routes
    latere lux env --compat anthropic   # Anthropic dialect, any model
    latere lux env --compat lux         # native dialect, any model

A [provider] argument is a passthrough: the provider is the route, so
only models it serves are reachable, and its own dialect applies.

    openai      OpenAI SDK      (OPENAI_BASE_URL, OPENAI_API_KEY)
    openrouter  OpenAI SDK      (same variables, OpenRouter route)
    local       OpenAI SDK      (your 'lux serve' tunnels)
    anthropic   Anthropic SDK   (ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN —
                not ANTHROPIC_API_KEY, which sends x-api-key and Lux ignores)

--compat drops the provider from the route entirely. Every model Lux
can reach is available, phrased in the dialect you picked. Name the
provider in the model id when a bare name would be ambiguous, e.g.
"openai/gpt-5" on the anthropic surface. The two cannot be combined:
env vars carry a base URL, not a model.

Gemini's SDK has no bearer path; use 'latere lux invoke' or an
OpenRouter route.

The embedded credential and its lifetime are reported on stderr (stdout
stays eval-clean). By default it is your login identity token, which
lasts the login session. For CI, --ttl mints a short-lived actor token
instead. Use a positive whole number of seconds, without --token or
LATERE_LUX_TOKEN. Auth may shorten the requested lifetime; stderr reports
the lifetime returned by auth.`,
		Example: `  eval "$(latere lux env openai)"
  eval "$(latere lux env --compat anthropic)"
  eval "$(latere lux env --compat lux)"
  eval "$(latere lux env --compat openai --ttl 5m)"
  TOKEN=$(latere lux env --raw)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("ttl") {
				if ttl <= 0 || ttl%time.Second != 0 {
					return errors.New("--ttl must be a positive whole number of seconds (e.g. 1s or 5m)")
				}
				if _, supplied := passthroughToken(*token); supplied {
					return errors.New("--ttl cannot be combined with --token or LATERE_LUX_TOKEN; use your saved login to mint an actor token")
				}
			}
			route := provider
			if len(args) == 1 {
				route = args[0]
			}
			spec, err := resolveEnvSurface(cmd.Context(), *luxURL, *authURL, *token, route, compatDialect(compat), raw)
			if err != nil {
				return err
			}
			bearer, provenance, err := luxEnvBearer(cmd.Context(), *token, *luxURL, *authURL, ttl)
			if err != nil {
				return err
			}
			if raw {
				fprintln(cmd.OutOrStdout(), bearer)
				fprintf(cmd.ErrOrStderr(), "# %s\n", provenance)
				return nil
			}
			base := strings.TrimRight(resolveLuxURL(*luxURL), "/")
			baseValue, err := quoteShellValue(base + spec.envBaseURL)
			if err != nil {
				return err
			}
			keyValue, err := quoteShellValue(bearer)
			if err != nil {
				return err
			}
			fprintf(cmd.OutOrStdout(), "export %s=%s\n", spec.envBaseVar, baseValue)
			fprintf(cmd.OutOrStdout(), "export %s=%s\n", spec.envKeyVar, keyValue)
			fprintf(cmd.ErrOrStderr(), "# %s\n", provenance)
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "deprecated alias for the [route] argument")
	_ = cmd.Flags().MarkHidden("provider")
	cmd.Flags().StringVar(&compat, "compat", "",
		"dialect to speak instead of a provider passthrough: "+strings.Join(compatDialectNames(), ", "))
	_ = cmd.RegisterFlagCompletionFunc("compat",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return compatDialectNames(), cobra.ShellCompDirectiveNoFileComp
		})
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "request an actor token lifetime in whole seconds (e.g. 5m); auth may shorten it")
	cmd.Flags().BoolVar(&raw, "raw", false, "print the bare token only, no exports")
	return cmd
}

// newLuxTokenCmd is a hidden deprecated alias: `lux env --raw` owns the
// bare-token job now. Kept functional so scripts don't break.
func newLuxTokenCmd(luxURL, authURL, token *string) *cobra.Command {
	return &cobra.Command{
		Use:    "token",
		Short:  "Deprecated alias for `lux env --raw`.",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bearer, err := luxIdentityBearer(cmd.Context(), *token, *luxURL, *authURL)
			if err != nil {
				return err
			}
			fmt.Println(bearer)
			return nil
		},
	}
}

// ---- invoke: chat ----

func newLuxChatCmd(luxURL, authURL, token *string) *cobra.Command {
	var (
		model     string
		provider  string
		maxTokens int
		jsonF     bool
	)
	cmd := &cobra.Command{
		Use:     "invoke <prompt>",
		Aliases: []string{"chat"},
		Short:   "Raw one-shot model call — for verifying Lux access and bindings.",
		Long: `Send one raw prompt to a model through Lux and print the reply.

This is a diagnostic, not an assistant: no tools, no session, no
workspace (the built-in equivalent of a curl against the gateway). Use
it to verify a model responds through your identity after 'lux access
set' or a provider binding. For actual assistant work, use
'latere topos -p "<prompt>"'.

The request goes through Lux with your identity as the bearer, so cost
is tracked on your identity and no key is allocated. The inference path
enforces no scope, so this works with any valid identity.

Supported providers: openai, openrouter, local (OpenAI
chat/completions) and anthropic (Messages API).

Pass a model id exactly as 'latere lux models' lists it. A tunneled
model listed as local/<model> can be called either way.`,
		Example: `  latere lux invoke --model openai/gpt-4o-mini "Say hi"
  latere lux invoke --provider anthropic --model claude-sonnet-4-6 "Say hi"
  latere lux invoke --model local/gemma4 "Say hi"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if model == "" {
				return errors.New("--model is required")
			}
			// Without an explicit --provider, resolve it from the caller's
			// catalog: `--model claude-sonnet-5` must reach anthropic, not
			// 404 on the openai default.
			if !cmd.Flags().Changed("provider") {
				if p, err := inferInvokeProvider(cmd.Context(), *luxURL, *authURL, *token, model); err != nil {
					return err
				} else if p != "" {
					provider = p
				}
			}
			spec, err := lookupProviderFor(cmd.Context(), *luxURL, *authURL, *token, provider)
			if err != nil {
				return err
			}
			if spec.chatPath == "" {
				return fmt.Errorf("`lux invoke` does not support %q; use openai, openrouter, or anthropic", provider)
			}
			prompt := strings.Join(args, " ")
			bearer, err := luxBearer(cmd.Context(), *token, *luxURL, *authURL)
			if err != nil {
				return err
			}
			base := strings.TrimRight(resolveLuxURL(*luxURL), "/")
			model = localWireModel(spec.name, model)

			var body map[string]any
			headers := map[string]string{}
			if spec.anthropicStyle {
				headers["anthropic-version"] = "2023-06-01"
				body = map[string]any{
					"model":      model,
					"max_tokens": maxTokens,
					"messages":   []map[string]any{{"role": "user", "content": prompt}},
				}
			} else {
				body = map[string]any{
					"model":    model,
					"messages": []map[string]any{{"role": "user", "content": prompt}},
				}
			}
			raw, err := luxPostJSON(cmd.Context(), base+spec.chatPath, bearer, headers, body)
			if err != nil {
				return wrapLuxErr(err)
			}
			if jsonF {
				_, err := fmt.Fprintln(os.Stdout, strings.TrimSpace(string(raw)))
				return err
			}
			text, err := extractChatText(raw, spec.anthropicStyle)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(os.Stdout, text)
			return err
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "model id to call (required)")
	cmd.Flags().StringVar(&provider, "provider", "openai",
		"provider route; defaults to the provider that owns the model in your catalog (see `latere lux providers`)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 1024, "max output tokens (Anthropic requires this)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the raw provider JSON response")
	return cmd
}

// extractChatText pulls the assistant text out of an OpenAI
// chat/completions or Anthropic messages response.
func extractChatText(raw []byte, anthropicStyle bool) (string, error) {
	if anthropicStyle {
		var resp struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", fmt.Errorf("parse response: %w", err)
		}
		var b strings.Builder
		for _, c := range resp.Content {
			if c.Text != "" {
				b.WriteString(c.Text)
			}
		}
		return strings.TrimSpace(b.String()), nil
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("model returned no choices")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// ---- usage ----

// usagePeriods maps the --period presets onto a window, the series
// interval Lux supports (hour/day/week), and the label format used to
// bucket points client-side (year re-buckets week points into months).
var usagePeriods = map[string]struct {
	window   time.Duration
	interval string
	labelFmt string
}{
	"day":     {24 * time.Hour, "hour", "15:00"},
	"week":    {7 * 24 * time.Hour, "day", "Mon Jan 02"},
	"month":   {30 * 24 * time.Hour, "day", "Jan 02"},
	"quarter": {90 * 24 * time.Hour, "week", "Jan 02"},
	"year":    {365 * 24 * time.Hour, "week", "Jan 2006"},
}

type luxUsageRow struct {
	Group        string `json:"group"`
	Calls        int64  `json:"calls"`
	TokensIn     int64  `json:"tokens_in"`
	TokensOut    int64  `json:"tokens_out"`
	CostUSDMicro int64  `json:"cost_usd_micro"`
}

type luxSeriesPoint struct {
	TS           time.Time `json:"ts"`
	Group        string    `json:"group"`
	Calls        int64     `json:"calls"`
	CostUSDMicro int64     `json:"cost_usd_micro"`
}

func newLuxUsageCmd(luxURL, authURL, token *string) *cobra.Command {
	var jsonF bool
	var period, groupBy string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Usage and cost overview for a period (day/week/month/quarter/year).",
		Long: `Show the usage and cost Lux recorded for your identity: the period
total, a per-model (or per-provider) breakdown, and a cost-over-time
bar chart.

Reads /lux/v1/usage and /lux/v1/usage/series.`,
		Example: `  latere lux usage
  latere lux usage --period week
  latere lux usage --period year --by provider
  latere lux usage --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ok := usagePeriods[period]
			if !ok {
				return fmt.Errorf("unknown --period %q; one of: day, week, month, quarter, year", period)
			}
			if groupBy != "model" && groupBy != "provider" {
				return fmt.Errorf("unknown --by %q; one of: model, provider", groupBy)
			}
			c, _, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}

			to := time.Now().UTC()
			from := to.Add(-p.window)
			rng := fmt.Sprintf("from=%s&to=%s", url.QueryEscape(from.Format(time.RFC3339)), url.QueryEscape(to.Format(time.RFC3339)))

			var groups struct {
				Items []luxUsageRow `json:"items"`
			}
			if err := c.GetJSON(cmd.Context(), "/lux/v1/usage?"+rng+"&group_by="+groupBy, &groups); err != nil {
				return wrapLuxErr(err)
			}
			var series struct {
				Items []luxSeriesPoint `json:"items"`
			}
			if err := c.GetJSON(cmd.Context(), "/lux/v1/usage/series?"+rng+"&group_by="+groupBy+"&interval="+p.interval, &series); err != nil {
				return wrapLuxErr(err)
			}

			if jsonF {
				return printJSON(map[string]any{
					"from": from, "to": to, "period": period, "group_by": groupBy,
					"groups": groups.Items, "series": series.Items,
				})
			}
			renderLuxUsage(cmd.OutOrStdout(), period, from, to, p.labelFmt, groups.Items, series.Items)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	cmd.Flags().StringVar(&period, "period", "month", "window: day, week, month, quarter, or year")
	cmd.Flags().StringVar(&groupBy, "by", "model", "breakdown dimension: model or provider")
	return cmd
}

// renderLuxUsage prints the period total, the per-group breakdown sorted
// by cost, and a cost bar chart bucketed by the period's label format.
func renderLuxUsage(w io.Writer, period string, from, to time.Time, labelFmt string, groups []luxUsageRow, series []luxSeriesPoint) {
	var totalCost, totalCalls int64
	for _, g := range groups {
		totalCost += g.CostUSDMicro
		totalCalls += g.Calls
	}
	fprintf(w, "Usage last %s (%s – %s): %s, %d calls\n\n",
		period, from.Format("Jan 02"), to.Format("Jan 02"), fmtUSDMicro(totalCost), totalCalls)
	if totalCalls == 0 {
		fprintln(w, "No usage in this period.")
		return
	}

	sortGroups := make([]luxUsageRow, len(groups))
	copy(sortGroups, groups)
	slices.SortFunc(sortGroups, func(a, b luxUsageRow) int {
		return int(b.CostUSDMicro - a.CostUSDMicro)
	})
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, g := range sortGroups {
		fprintf(tw, "  %s\t%s\t%d calls\t%s in / %s out\n",
			g.Group, fmtUSDMicro(g.CostUSDMicro), g.Calls, fmtTokens(g.TokensIn), fmtTokens(g.TokensOut))
	}
	_ = tw.Flush()

	// Bucket the series by label (sums across groups); labels keep their
	// first-seen chronological order.
	type bucket struct {
		label string
		cost  int64
	}
	var buckets []bucket
	idx := map[string]int{}
	for _, pt := range series {
		label := pt.TS.Format(labelFmt)
		i, ok := idx[label]
		if !ok {
			i = len(buckets)
			idx[label] = i
			buckets = append(buckets, bucket{label: label})
		}
		buckets[i].cost += pt.CostUSDMicro
	}
	var maxCost int64
	for _, b := range buckets {
		maxCost = max(maxCost, b.cost)
	}
	if maxCost == 0 {
		return
	}
	fprintln(w)
	const width = 28
	for _, b := range buckets {
		n := int(b.cost * width / maxCost)
		if n == 0 && b.cost > 0 {
			n = 1
		}
		fprintf(w, "  %-12s %-*s %s\n", b.label, width, strings.Repeat("▇", n), fmtUSDMicro(b.cost))
	}
}

// fmtUSDMicro renders micro-USD as dollars, keeping sub-cent amounts
// readable ($0.0034 rather than $0.00).
func fmtUSDMicro(micro int64) string {
	v := float64(micro) / 1e6
	if v != 0 && v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// fmtTokens renders token counts compactly (1.2K, 3.4M).
func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ---- access profile ----

func newLuxAccessCmd(luxURL, authURL, token *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "View or set your Lux access profile (model bindings).",
		Long: `Inspect and self-provision your Lux access profile.

A model resolves through a provider key you can reach: one you registered
yourself, or a Latere platform key you hold a grant on. Bindings are a
routing override on top of that, not a permission. If you own exactly one
key, you likely need no bindings at all.

'show' prints the current profile, 'set' pins one model to a provider key,
'clear' removes every binding. Platform-granted models need no binding
here; they appear in 'latere lux models' automatically.`,
	}
	cmd.AddCommand(newLuxAccessShowCmd(luxURL, authURL, token))
	cmd.AddCommand(newLuxAccessSetCmd(luxURL, authURL, token))
	cmd.AddCommand(newLuxAccessClearCmd(luxURL, authURL, token))
	return cmd
}

func newLuxAccessShowCmd(luxURL, authURL, token *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show your Lux access profile.",
		Long:  "Print your access profile: bindings, allowlist, spend cap, rate limits (GET /lux/v1/me/profile).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			var out json.RawMessage
			if err := c.GetJSON(cmd.Context(), "/lux/v1/me/profile", &out); err != nil {
				return wrapLuxErr(err)
			}
			return printJSON(out)
		},
	}
}

func newLuxAccessSetCmd(luxURL, authURL, token *string) *cobra.Command {
	var (
		model       string
		provider    string
		providerKey string
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Bind a model to a provider key in your access profile.",
		Long: `Bind a client-facing model to a provider key so paid calls resolve.

PATCHes /lux/v1/me/profile with a single-target 'fallback' binding.
The provider key must be one you can use (your own registered key, or a
platform key).`,
		Example: `  latere lux access set --model gpt-5 --provider openai --provider-key <provider-key-id>`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if model == "" || provider == "" || providerKey == "" {
				return errors.New("--model, --provider, and --provider-key are required")
			}
			c, _, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			bindings := map[string]any{
				"models": map[string]any{
					model: map[string]any{
						"strategy": "fallback",
						"targets": []map[string]any{{
							"provider":        provider,
							"model":           model,
							"provider_key_id": providerKey,
						}},
					},
				},
			}
			rawBindings, err := json.Marshal(bindings)
			if err != nil {
				return err
			}
			b, err := json.Marshal(map[string]any{"bindings": json.RawMessage(rawBindings)})
			if err != nil {
				return err
			}
			var out json.RawMessage
			if err := c.Do(cmd.Context(), http.MethodPatch, "/lux/v1/me/profile",
				bytes.NewReader(b), "application/json", &out); err != nil {
				return wrapLuxErr(err)
			}
			fmt.Fprintf(os.Stderr, "Bound %s -> %s (provider key %s).\n", model, provider, providerKey)
			return printJSON(out)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "client-facing model id (required)")
	cmd.Flags().StringVar(&provider, "provider", "", "provider for the target, e.g. openai (required)")
	cmd.Flags().StringVar(&providerKey, "provider-key", "", "provider key id to route through (required)")
	return cmd
}

// newLuxAccessClearCmd is the inverse of 'set'.
//
// It clears the whole bindings map rather than one model, which mirrors
// how 'set' already behaves: the PATCH replaces bindings wholesale, so a
// 'set' overwrites every existing binding anyway. Removing a single model
// would need a read-modify-write with a lost-update race, and would be
// finer-grained than the only command that writes bindings.
//
// Clearing is safe for a principal: an unbound model falls through to an
// owned provider key. A virtual key's bindings are its containment
// boundary, but those are fixed at mint time and not reachable through
// this endpoint.
func newLuxAccessClearCmd(luxURL, authURL, token *string) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Clear every model binding from your access profile.",
		Long: `Remove all model bindings, leaving routing to your own provider keys.

PATCHes /lux/v1/me/profile with an empty bindings map. Your provider keys,
spend cap, and rate limits are untouched; only the routing overrides go.

Models keep working: an unbound model resolves through a provider key you
own. Bindings only matter when you need to choose between several keys,
order failover, or alias a model name.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			patch := map[string]any{"bindings": map[string]any{}}
			b, _ := json.Marshal(patch)
			var out json.RawMessage
			if err := c.Do(cmd.Context(), http.MethodPatch, "/lux/v1/me/profile",
				bytes.NewReader(b), "application/json", &out); err != nil {
				return wrapLuxErr(err)
			}
			fmt.Fprintln(os.Stderr, "Cleared all model bindings.")
			return printJSON(out)
		},
	}
}

// ---- bearer acquisition ----

// resolveLuxURL returns the Lux base URL: flag > LUX_API_URL > default.
func resolveLuxURL(flagURL string) string {
	if flagURL != "" {
		return flagURL
	}
	if v := os.Getenv("LUX_API_URL"); v != "" {
		return v
	}
	return "https://lux.latere.ai"
}

// passthroughToken returns a caller-supplied bearer and true when one is
// available: an explicit --token, then LATERE_LUX_TOKEN. These are
// presented to Lux verbatim (Lux validates them). When none is present,
// the bearer is derived from the retained auth login.
func passthroughToken(tokenFlag string) (string, bool) {
	if t := strings.TrimSpace(tokenFlag); t != "" {
		return t, true
	}
	if t := strings.TrimSpace(os.Getenv("LATERE_LUX_TOKEN")); t != "" {
		return t, true
	}
	return "", false
}

// authIdentityToken loads the retained auth.latere.ai root token,
// refreshing it when expired, and returns the access token plus the
// resolved auth base. This token IS the caller's identity bearer; Lux
// accepts it directly (it validates the auth issuer and does not check
// the audience).
func authIdentityToken(ctx context.Context, luxURL, authURLFlag string) (access, authBase string, err error) {
	authTok, err := api.LoadAuthToken()
	if err != nil {
		if errors.Is(err, api.ErrNoToken) {
			return "", "", fmt.Errorf("cannot authenticate to Lux: %w", err)
		}
		return "", "", err
	}
	authBase = resolveAuthURL(resolveLuxURL(luxURL), authURLFlag)

	access = authTok.AccessToken
	if authTok.RefreshToken == "" && !authTok.ExpiresAt.IsZero() && !time.Now().Before(authTok.ExpiresAt) {
		return "", "", errors.New("auth token expired without a refresh token; run `latere login`")
	}
	// Refresh when the token is known to be expired (or within a small
	// skew). A zero ExpiresAt means "unknown"; skip refresh and let the
	// downstream call surface a re-login error if it is in fact expired.
	if authTok.RefreshToken != "" && !authTok.ExpiresAt.IsZero() &&
		time.Now().After(authTok.ExpiresAt.Add(-60*time.Second)) {
		refreshed, rerr := api.RefreshAuthToken(ctx, authBase, authTok)
		if rerr != nil {
			return "", "", fmt.Errorf("auth token expired and refresh failed (%w); run `latere login`", rerr)
		}
		access = refreshed.AccessToken
	}
	if access == "" {
		return "", "", errors.New("saved auth credential has no access token; run `latere login`")
	}
	return access, authBase, nil
}

// luxIdentityBearer returns the identity bearer handed to an external SDK
// by `lux env` / `lux token`: a passthrough token, or the retained auth
// identity token (refreshed). It is the longest-lived bearer the CLI
// emits — it lasts the auth token's lifetime so an SDK session survives
// (re-run to refresh), unlike a 5-minute actor token.
func luxIdentityBearer(ctx context.Context, tokenFlag, luxURL, authURL string) (string, error) {
	if t, ok := passthroughToken(tokenFlag); ok {
		return t, nil
	}
	access, _, err := authIdentityToken(ctx, luxURL, authURL)
	return access, err
}

// luxBearer returns a short-lived bearer for a single CLI-initiated call
// (chat, discovery, usage, access): a passthrough token, or a freshly
// minted aud=lux.latere.ai actor token (≤5 min, audience-bound — the
// call completes in seconds, so the short TTL costs nothing and bounds a
// leaked value).
func luxBearer(ctx context.Context, tokenFlag, luxURL, authURL string) (string, error) {
	if t, ok := passthroughToken(tokenFlag); ok {
		return t, nil
	}
	access, authBase, err := authIdentityToken(ctx, luxURL, authURL)
	if err != nil {
		return "", err
	}
	httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil)}
	bearer, err := api.MintActorToken(ctx, httpc, authBase, access, "lux.latere.ai", 300)
	if err != nil {
		return "", fmt.Errorf("mint Lux token: %w; if this persists run `latere login`", err)
	}
	return bearer, nil
}

// inferInvokeProvider resolves which provider serves model by looking it
// up in the caller's catalog. A model reachable both natively and via
// OpenRouter prefers the native route. An unknown model is a hard error
// pointing at `lux models`; a catalog fetch failure falls back to ""
// (caller keeps its default) so scope-limited tokens can still invoke.
func inferInvokeProvider(ctx context.Context, luxURL, authURL, token, model string) (string, error) {
	c, _, err := luxClient(ctx, luxURL, authURL, token)
	if err != nil {
		return "", err
	}
	var resp luxCatalogResponse
	if err := c.GetJSON(ctx, "/lux/v1/models", &resp); err != nil {
		fmt.Fprintf(os.Stderr, "note: could not infer the provider from your catalog (%v); assuming openai — pass --provider to override\n", err)
		return "", nil
	}
	var providers []string
	for _, it := range resp.Items {
		if m, _ := it["model"].(string); m == model {
			if p, _ := it["provider"].(string); p != "" && !slices.Contains(providers, p) {
				providers = append(providers, p)
			}
		}
	}
	// Nothing matched the id as typed: the catalog lists a tunneled model
	// under the prefixed id `local/<model>` (its row id), and that is the
	// id a user copies. Resolve it to the local route. Exact model matches
	// win first, so a provider whose model id genuinely contains a slash
	// (openrouter's `anthropic/claude-...`) is never shadowed.
	if len(providers) == 0 && strings.HasPrefix(model, localProviderName+"/") {
		bare := strings.TrimPrefix(model, localProviderName+"/")
		for _, it := range resp.Items {
			p, _ := it["provider"].(string)
			if m, _ := it["model"].(string); p == localProviderName && m == bare {
				providers = append(providers, localProviderName)
				break
			}
		}
	}
	switch len(providers) {
	case 0:
		return "", fmt.Errorf("model %q is not in your catalog; run `latere lux models` to see what you can call", model)
	case 1:
		return providers[0], nil
	default:
		for _, p := range providers {
			if p != "openrouter" {
				return p, nil
			}
		}
		return providers[0], nil
	}
}

// luxClient builds an authenticated API client pointed at Lux, using the
// resolved identity bearer (not the stored Cella token). The bearer is
// also returned for callers that need to present it directly.
func luxClient(ctx context.Context, luxURL, authURL, tokenFlag string) (*api.Client, string, error) {
	bearer, err := luxBearer(ctx, tokenFlag, luxURL, authURL)
	if err != nil {
		return nil, "", err
	}
	c := api.NewClient(resolveLuxURL(luxURL))
	c.Refresh = nil  // luxBearer handles auth refresh; never exchange for Cella here.
	c.Token = bearer // override NewClient's auto-loaded Cella token
	return c, bearer, nil
}

// luxPostJSON POSTs a JSON body with the bearer and any extra headers,
// returning the raw response body. Non-2xx responses become an
// *api.APIError so wrapLuxErr can render a friendly message.
func luxPostJSON(ctx context.Context, url, bearer string, headers map[string]string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", "latere-cli")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second, Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	const maxResponse = 8 << 20
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if resp.StatusCode/100 != 2 {
		// Keep HTTP failures structured, with bounded diagnostic text.
		if len(respBody) > maxResponse {
			respBody = respBody[:maxResponse]
		}
		e := &api.APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(respBody))}
		_ = json.Unmarshal(respBody, e)
		if e.Code == "" {
			// Provider-shaped envelope ({"error":{"type","code","message"}}),
			// which the inference routes emit. Without this the caller sees
			// the raw JSON blob instead of the code and message.
			var nested struct {
				Error struct {
					Code    string `json:"code"`
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(respBody, &nested) == nil {
				if nested.Error.Code != "" {
					e.Code = nested.Error.Code
				} else if nested.Error.Type != "" {
					e.Code = nested.Error.Type
				}
				if nested.Error.Message != "" {
					e.Message = nested.Error.Message
				}
			}
		}
		return nil, e
	}
	if readErr != nil {
		return nil, fmt.Errorf("read Lux response: %w", readErr)
	}
	if len(respBody) > maxResponse {
		return nil, errors.New("response from Lux exceeds 8 MiB limit")
	}
	return respBody, nil
}

// ---- friendly errors ----

// wrapLuxErr turns Lux's forbidden / not-bound envelopes into actionable
// guidance, leaving other errors untouched.
func wrapLuxErr(err error) error {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.Code == "auth.forbidden":
		return fmt.Errorf(
			"access denied by Lux (%s).\n"+
				"Your login may not have access here. Run `latere login` to refresh your session, or ask a Latere admin for access",
			apiErr.Message)
	case apiErr.Code == "tunnel.offline":
		return fmt.Errorf(
			"no live tunnel serves this model (%s).\n"+
				"Start it on the machine that hosts the runtime with `latere lux serve`, then check `latere lux models`",
			apiErr.Message)
	case strings.Contains(apiErr.Code, "provider_not_bound"):
		return fmt.Errorf(
			"no provider is bound for this model, so Lux can't route it (%s).\n"+
				"Bind it with `latere lux access set --model <m> --provider <p> --provider-key <id>`,\n"+
				"or ask a Latere admin for a platform grant that covers it",
			apiErr.Message)
	}
	return err
}

// decodeJWTClaims best-effort decodes a JWT payload. Returns nil when the
// input is not a three-part JWT or the payload isn't JSON.
func decodeJWTClaims(raw string) map[string]any {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}
