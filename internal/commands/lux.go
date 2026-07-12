package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"latere.ai/x/pkg/oidc"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/tunnel"
)

// Lux is the Latere model gateway at lux.latere.ai. These commands let
// the CLI call models with the user's identity (or a sandbox service
// identity) instead of an allocated key: cost is tracked on the
// identity. See lux/specs/11-cli-keyless-access.md.

// sandboxTokenFile is where Cella projects a sandbox's trust-plane
// egress token. When present (and no explicit token is given), `latere
// lux` uses it as a service identity — overridable for testing.
var sandboxTokenFile = "/run/cella/sandbox-token/token"

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
			// OpenRouter route. Best keyless lane (free models live here).
			name: "openrouter", chatPath: "/openrouter/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/openrouter/v1",
		},
		"anthropic": {
			name: "anthropic", chatPath: "/anthropic/v1/messages", anthropicStyle: true,
			// The Anthropic SDK appends /v1/messages, so the base is the
			// bare /anthropic prefix. ANTHROPIC_AUTH_TOKEN -> bearer.
			envBaseVar: "ANTHROPIC_BASE_URL", envKeyVar: "ANTHROPIC_AUTH_TOKEN", envBaseURL: "/anthropic",
		},
		"gemini": {
			// Reachable via `lux invoke`-style direct call is non-trivial
			// (generateContent), and the Gemini SDK has no bearer path,
			// so neither chat nor env is offered here.
			name: "gemini",
		},
		"local": {
			// Local runtimes tunneled in via `lux serve` (spec 18). They
			// speak the openai-compat dialect, so the OpenAI SDK pointed at
			// the /local/v1 route just works. No upstream key.
			name: "local", chatPath: "/local/v1/chat/completions",
			envBaseVar: "OPENAI_BASE_URL", envKeyVar: "OPENAI_API_KEY", envBaseURL: "/local/v1",
		},
	}
}

func lookupProvider(name string) (providerSpec, error) {
	p, ok := providerSpecs()[name]
	if !ok {
		return providerSpec{}, fmt.Errorf("unknown provider %q; one of: openai, openrouter, anthropic, gemini", name)
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
CLI presents your identity — your user login, or a sandbox's service
token — and Lux tracks cost on that identity. Inside a sandbox with a
'lux' trust plane, the projected token at /run/cella/sandbox-token/token
is used automatically.

Run 'latere login' first (it now requests the llm.read / llm.invoke
scopes Lux needs). The base URL defaults to https://lux.latere.ai and
can be overridden by LUX_API_URL or --lux-url.

Free models (OpenRouter ':free') work with no setup. Paid models need an
access profile binding to a provider key — see 'latere lux access'.`,
		Example: `  latere lux models
  eval "$(latere lux env --provider openai)"
  latere lux invoke --model openai/gpt-4o-mini "Say hi"
  latere lux usage`,
	}
	cmd.PersistentFlags().StringVar(&luxURL, "lux-url", "", "override Lux base URL (overrides LUX_API_URL)")
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override auth base URL (default derived from the Lux URL)")
	cmd.PersistentFlags().StringVar(&token, "token", "", "present this bearer to Lux instead of minting one (e.g. a sandbox token)")

	cmd.AddCommand(newLuxModelsCmd(&luxURL, &authURL, &token))
	cmd.AddCommand(newLuxCatalogCmd("providers", "/lux/v1/providers", "providers Lux can route to", &luxURL, &authURL, &token))
	// Deprecated: rates ride `lux models` now. Hidden but functional so
	// scripts keep working.
	rates := newLuxCatalogCmd("rates", "/lux/v1/rates", "model rate card", &luxURL, &authURL, &token)
	rates.Hidden = true
	cmd.AddCommand(rates)
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
same gates and request log as any other Lux model (lux spec 18).

This runs a long-lived outbound connection (no inbound port is opened) and
forwards inbound requests only to the configured local runtime. Requires
the llm.serve scope (run 'latere login' to refresh your scopes).

Discoverable as local/<model> in 'latere lux models'. Call it by pointing
an OpenAI-compatible SDK at <lux>/local/v1.`,
		Example: `  latere lux serve
  latere lux serve --runtime vllm
  latere lux serve --upstream http://localhost:1234 --models llama3.1:8b
  latere lux serve --share org`,
		RunE: func(cmd *cobra.Command, args []string) error {
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

// bearerHasOrg reports whether a JWT bearer carries a non-empty org_id
// claim. A non-JWT (e.g. sandbox) token returns false.
func bearerHasOrg(bearer string) bool {
	return strings.TrimSpace(stringClaim(decodeJWTClaims(bearer), "org_id")) != ""
}

// splitCSV splits a comma-separated list, trimming spaces and dropping
// empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
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

Reads /lux/v1/models and /lux/v1/rates with your identity. Requires the
llm.read scope.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, bearer, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			if err := ensureLuxScope(bearer, []string{"llm.read", "llm.invoke"}, "list models"); err != nil {
				return err
			}
			var models luxCatalogResponse
			if err := c.GetJSON(cmd.Context(), "/lux/v1/models", &models); err != nil {
				return wrapLuxErr(err)
			}
			var rates luxCatalogResponse
			if err := c.GetJSON(cmd.Context(), "/lux/v1/rates", &rates); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: rate card unavailable (%v)\n", err)
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
	in, okIn := m["input_usd_per_m"]
	out, okOut := m["output_usd_per_m"]
	if !okIn || !okOut {
		return ""
	}
	s := fmt.Sprintf("$%v/M in, $%v/M out", in, out)
	if c, ok := m["input_cached_usd_per_m"]; ok {
		s += fmt.Sprintf(" ($%v/M cached input)", c)
	}
	return s
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
		Long:  fmt.Sprintf("List %s.\n\nReads %s with your identity. Requires the llm.read scope.", what, path),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, bearer, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			if err := ensureLuxScope(bearer, []string{"llm.read", "llm.invoke"}, "list "+name); err != nil {
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
			fmt.Fprintln(os.Stdout)
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

func newLuxEnvCmd(luxURL, authURL, token *string) *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print shell exports that point a stock SDK at Lux (keyless).",
		Long: `Print 'export' lines that point a provider SDK at Lux using your
identity as the bearer — no key allocation.

    eval "$(latere lux env --provider openai)"

Then a normal OpenAI SDK call is routed through Lux and billed to your
identity. The printed token is your identity token; it lasts the login
session — re-run this when it expires.

Supported: openai, openrouter (OpenAI SDK; the credential is sent as a
bearer), and anthropic (via ANTHROPIC_AUTH_TOKEN, the SDK's bearer knob;
ANTHROPIC_API_KEY would send x-api-key, which Lux ignores). Gemini's SDK
has no bearer path; use 'latere lux invoke' or an OpenRouter route.`,
		Example: `  eval "$(latere lux env --provider openai)"
  eval "$(latere lux env --provider anthropic)"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := lookupProvider(provider)
			if err != nil {
				return err
			}
			if spec.envBaseVar == "" {
				return fmt.Errorf("`lux env` does not support %q (its SDK has no bearer path); use `lux invoke` or --provider openrouter", provider)
			}
			base := strings.TrimRight(resolveLuxURL(*luxURL), "/")
			bearer, err := luxIdentityBearer(cmd.Context(), *token, *luxURL, *authURL)
			if err != nil {
				return err
			}
			fmt.Printf("export %s=%s\n", spec.envBaseVar, base+spec.envBaseURL)
			fmt.Printf("export %s=%s\n", spec.envKeyVar, bearer)
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "openai", "provider SDK to configure: openai|openrouter|anthropic")
	return cmd
}

func newLuxTokenCmd(luxURL, authURL, token *string) *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print a Lux identity bearer token to stdout.",
		Long: `Print the identity bearer the CLI presents to Lux (your auth identity
token, or a sandbox token when running inside a sandbox). It lasts the
login session. Useful for scripting:

    TOKEN=$(latere lux token)
    curl -H "Authorization: Bearer $TOKEN" https://lux.latere.ai/lux/v1/models`,
		Args: cobra.NoArgs,
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
workspace — the built-in equivalent of a curl against the gateway. Use
it to verify a model responds through your identity after 'lux access
set' or a provider binding. For actual assistant work, use
'latere topos -p "<prompt>"'.

The request goes through Lux with your identity as the bearer, so cost
is tracked on your identity and no key is allocated. The inference path
enforces no scope, so this works with any valid identity (including a
sandbox service token).

Supported providers: openai, openrouter (OpenAI chat/completions) and
anthropic (Messages API).`,
		Example: `  latere lux invoke --model openai/gpt-4o-mini "Say hi"
  latere lux invoke --provider anthropic --model claude-sonnet-4-6 "Say hi"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if model == "" {
				return errors.New("--model is required")
			}
			spec, err := lookupProvider(provider)
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
				fmt.Println(strings.TrimSpace(string(raw)))
				return nil
			}
			text, err := extractChatText(raw, spec.anthropicStyle)
			if err != nil {
				return err
			}
			fmt.Println(text)
			return nil
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "model id to call (required)")
	cmd.Flags().StringVar(&provider, "provider", "openai", "provider route: openai|openrouter|anthropic")
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

func newLuxUsageCmd(luxURL, authURL, token *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show model usage and cost attributed to your identity.",
		Long:  "Show usage and cost recorded by Lux for your identity (GET /lux/v1/me/usage). Requires the llm.read scope.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, bearer, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			if err := ensureLuxScope(bearer, []string{"llm.read", "llm.invoke"}, "read usage"); err != nil {
				return err
			}
			var out json.RawMessage
			if err := c.GetJSON(cmd.Context(), "/lux/v1/me/usage", &out); err != nil {
				return wrapLuxErr(err)
			}
			// The usage shape is backend-defined; pretty JSON is the only
			// stable view, so --json (kept for compatibility) is the default.
			return printJSON(out)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// ---- access profile ----

func newLuxAccessCmd(luxURL, authURL, token *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "View or set your Lux access profile (model bindings).",
		Long: `Inspect and self-provision your Lux access profile.

A paid model resolves only when your identity is bound to a provider key
(an operator-managed platform key, or your own registered key). Free
models need no binding. 'show' prints the current profile; 'set' binds a
model to a provider key.`,
	}
	cmd.AddCommand(newLuxAccessShowCmd(luxURL, authURL, token))
	cmd.AddCommand(newLuxAccessSetCmd(luxURL, authURL, token))
	return cmd
}

func newLuxAccessShowCmd(luxURL, authURL, token *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show your Lux access profile.",
		Long:  "Print your access profile: bindings, allowlist, spend cap, rate limits (GET /lux/v1/me/profile).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, bearer, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			if err := ensureLuxScope(bearer, []string{"llm.read", "llm.invoke"}, "read your access profile"); err != nil {
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
Requires the llm.invoke scope. The provider key must be one you can use
(your own registered key, or a platform key).`,
		Example: `  latere lux access set --model gpt-5 --provider openai --provider-key <provider-key-id>`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if model == "" || provider == "" || providerKey == "" {
				return errors.New("--model, --provider, and --provider-key are required")
			}
			c, bearer, err := luxClient(cmd.Context(), *luxURL, *authURL, *token)
			if err != nil {
				return err
			}
			if err := ensureLuxScope(bearer, []string{"llm.invoke"}, "set your access profile"); err != nil {
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
			rawBindings, _ := json.Marshal(bindings)
			patch := map[string]any{"bindings": json.RawMessage(rawBindings)}
			b, _ := json.Marshal(patch)
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
// available: an explicit --token, then LATERE_LUX_TOKEN, then a sandbox
// service-identity token at /run/cella/sandbox-token/token. These are
// presented to Lux verbatim (Lux validates them). When none is present,
// the bearer is derived from the retained auth login.
func passthroughToken(tokenFlag string) (string, bool) {
	if t := strings.TrimSpace(tokenFlag); t != "" {
		return t, true
	}
	if t := strings.TrimSpace(os.Getenv("LATERE_LUX_TOKEN")); t != "" {
		return t, true
	}
	if b, err := os.ReadFile(sandboxTokenFile); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, true
		}
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
			return "", "", errors.New("not signed in for Lux; run `latere login` (it grants the llm.* scopes Lux needs)")
		}
		return "", "", err
	}
	authBase = authURLFlag
	if authBase == "" {
		authBase = inferAuthURL(resolveLuxURL(luxURL))
	}
	authBase = strings.TrimRight(authBase, "/")

	access = authTok.AccessToken
	// Refresh when the token is known to be expired (or within a small
	// skew). A zero ExpiresAt means "unknown"; skip refresh and let the
	// downstream call surface a re-login error if it is in fact expired.
	if authTok.RefreshToken != "" && !authTok.ExpiresAt.IsZero() &&
		time.Now().After(authTok.ExpiresAt.Add(-60*time.Second)) {
		refreshed, rerr := refreshAuthToken(ctx, authBase, authTok.RefreshToken)
		if rerr != nil {
			return "", "", fmt.Errorf("auth token expired and refresh failed (%v); run `latere login`", rerr)
		}
		access = refreshed.AccessToken
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
	httpc := &http.Client{Timeout: 15 * time.Second}
	bearer, err := mintActorToken(ctx, httpc, authBase, access, "lux.latere.ai", 300)
	if err != nil {
		return "", fmt.Errorf("mint Lux token: %w; if this persists run `latere login`", err)
	}
	return bearer, nil
}

// refreshAuthToken refreshes the retained auth root token and persists
// the result, preserving the previous refresh token when the response
// omits a new one (a common OAuth behaviour).
func refreshAuthToken(ctx context.Context, authBase, refreshToken string) (api.Token, error) {
	client := oidc.New(oidc.Config{
		AuthURL:  authBase,
		ClientID: "latere-cli",
		Scopes:   []string{"openid", "email", "profile", "offline_access", "llm.read", "llm.invoke", "llm.serve", "run:agents", "read:agents", "write:agents"},
	})
	if client == nil {
		return api.Token{}, errors.New("oidc: missing AuthURL or ClientID")
	}
	tok, err := client.RefreshTokenContext(ctx, refreshToken)
	if err != nil {
		return api.Token{}, err
	}
	out := api.Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    tok.Expiry,
		IssuedAt:     time.Now().UTC(),
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	_ = api.SaveAuthToken(out) // best-effort; the in-memory token still works this run
	return out, nil
}

// luxClient builds an authenticated API client pointed at Lux, using the
// resolved identity bearer (not the stored Cella token). Returns the
// bearer too so callers can preflight scopes.
func luxClient(ctx context.Context, luxURL, authURL, tokenFlag string) (*api.Client, string, error) {
	bearer, err := luxBearer(ctx, tokenFlag, luxURL, authURL)
	if err != nil {
		return nil, "", err
	}
	c := api.NewClient(resolveLuxURL(luxURL))
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
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		e := &api.APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(respBody))}
		_ = json.Unmarshal(respBody, e)
		return nil, e
	}
	return respBody, nil
}

// ---- scope preflight + friendly errors ----

// ensureLuxScope returns a friendly, specific error when the bearer is
// missing every scope in anyOf (llm.admin always satisfies). When the
// token can't be introspected (not a JWT, e.g. an opaque pasted token),
// the check is skipped and Lux remains the authority.
func ensureLuxScope(bearer string, anyOf []string, action string) error {
	claims := decodeJWTClaims(bearer)
	if claims == nil {
		return nil // can't introspect; defer to the server
	}
	scopes := scopesClaim(claims)
	want := append([]string{"llm.admin"}, anyOf...)
	for _, s := range scopes {
		for _, w := range want {
			if s == w {
				return nil
			}
		}
	}
	missing := strings.Join(anyOf, " or ")
	if stringClaim(claims, "kind") == "sandbox" {
		return fmt.Errorf(
			"this is a sandbox/service identity (scope trust-plane:egress): it can invoke models but can't %s, which needs %s.\n"+
				"Use a user login (`latere login`) for catalog, usage, and access commands.",
			action, missing)
	}
	return fmt.Errorf(
		"your Lux token is missing the %s scope needed to %s.\n"+
			"Re-run `latere login` and approve LLM access when prompted. If your organization hasn't\n"+
			"enabled LLM access for the CLI yet, ask an admin to grant llm.read / llm.invoke to the \"latere-cli\" client.",
		missing, action)
}

// wrapLuxErr turns Lux's forbidden / not-bound envelopes into actionable
// guidance, leaving other errors untouched.
func wrapLuxErr(err error) error {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.Code == "auth.forbidden" || (apiErr.Status == http.StatusForbidden && strings.Contains(apiErr.Message, "missing llm")):
		return fmt.Errorf(
			"Lux rejected the token for missing an llm.* scope (%s).\n"+
				"Re-run `latere login` and approve LLM access, or ask an admin to grant llm.read / llm.invoke to the \"latere-cli\" client.",
			apiErr.Message)
	case strings.Contains(apiErr.Code, "provider_not_bound"):
		return fmt.Errorf(
			"no provider is bound for this model, so Lux can't route it (%s).\n"+
				"Free models (OpenRouter ':free') work with no setup. For a paid model, bind it with\n"+
				"`latere lux access set --model <m> --provider <p> --provider-key <id>`, or ask an operator to\n"+
				"enable a platform key for your identity.",
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
