// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/llm"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The model commands reach the Lux core behind the platform origin, under
// llm.BasePath. The core serves one door per wire dialect under that base.
// The CLI's own calls, the model list and a model call, go through the
// SDK's llm package, which speaks the Latere dialect. The OpenAI and
// Anthropic doors, which `models env` points a stock SDK at, reach every
// Model in the catalog, translated where its provider speaks the other
// dialect; the Gemini door is never translated, and the catalog serves no
// Model in that dialect, so it reaches none. The credential of every call
// is the model key (models_key.go).

// defaultModelsURL is the base of the core's doors at the origin.
const defaultModelsURL = latere.DefaultBaseURL + llm.BasePath

// modelsTimeout bounds one model call, its retries included.
const modelsTimeout = 120 * time.Second

// defaultCatalogModel is the Model review's critics and the local Topos
// agent call when none is named. Models are named as the catalog names
// them, the provider in front, which also picks the door the agent's
// requests take: an anthropic model goes to the Anthropic door.
const defaultCatalogModel = "anthropic/claude-sonnet-5.5"

// openAIDoor is the core's OpenAI door under the base, which a stock
// OpenAI SDK calls a Model of any provider through.
const openAIDoor = "/openai"

// resolveModelsURL returns the base of the core's doors: the flag, then
// LATERE_MODELS_URL, then the origin.
func resolveModelsURL(flagURL string) string {
	u := flagURL
	if u == "" {
		u = os.Getenv("LATERE_MODELS_URL")
	}
	if u == "" {
		u = defaultModelsURL
	}
	return strings.TrimRight(u, "/")
}

// sdkDoor is what a stock SDK reads to call a door: the base URL variable,
// the key variable, and the door's path under the base, as the SDK expects
// it (the OpenAI SDK appends /chat/completions to a base ending in /v1; the
// Anthropic SDK appends its own version segment).
type sdkDoor struct {
	name, baseVar, keyVar, suffix string
}

// sdkDoors are the doors `models env` exports, in the order it names them:
// the doors that reach the catalog. The Anthropic key goes in
// ANTHROPIC_API_KEY, which the SDK sends as x-api-key; the core accepts the
// key in x-api-key, x-goog-api-key or Authorization.
var sdkDoors = []sdkDoor{
	{name: "openai", baseVar: "OPENAI_BASE_URL", keyVar: "OPENAI_API_KEY", suffix: openAIDoor + "/v1"},
	{name: "anthropic", baseVar: "ANTHROPIC_BASE_URL", keyVar: "ANTHROPIC_API_KEY", suffix: "/anthropic"},
}

// unreachingDoors are the doors the core serves that reach no Model of the
// catalog, each with the sentence a person who names it reads. The Gemini
// door is never translated, and every catalog Model is served in the
// OpenAI dialect.
var unreachingDoors = map[string]string{
	"gemini": "the Gemini door reaches no model of the Latere catalog, which is served in the OpenAI dialect; use --provider openai or --provider anthropic, which reach every catalog model, Google's included",
}

func sdkDoorNames() []string {
	names := make([]string, 0, len(sdkDoors))
	for _, d := range sdkDoors {
		names = append(names, d.name)
	}
	return names
}

func newModelsCmd() *cobra.Command {
	var modelsURL, authURL string
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List, call, and point SDKs at the Latere models your key reaches.",
		Long: `List, call, and point SDKs at the models of the Latere API
(https://api.latere.ai/v1/models).

Every call presents your model key: a key the CLI creates at auth the first
time a model command runs in your current context (your personal account,
or the organization 'latere org' selected), and keeps in the system
keychain. 'latere models key' shows it. Run 'latere login' first.

With no subcommand, 'latere models' lists the models your key reaches.
Models are named as the catalog names them, with the provider in front:
anthropic/claude-sonnet-5.5, openai/gpt-4.1-mini.

The base URL defaults to https://api.latere.ai/v1/models; LATERE_MODELS_URL
or --models-url overrides it.`,
		Example: `  latere models
  eval "$(latere models env)"
  latere models invoke --model anthropic/claude-sonnet-5.5 "Say hi"
  latere models key`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsList(cmd, modelsURL, authURL, jsonF)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	cmd.PersistentFlags().StringVar(&modelsURL, "models-url", "", "override the models base URL (overrides LATERE_MODELS_URL)")
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the models URL)")
	cmd.AddCommand(newModelsListCmd(&modelsURL, &authURL))
	cmd.AddCommand(newModelsEnvCmd(&modelsURL, &authURL))
	cmd.AddCommand(newModelsInvokeCmd(&modelsURL, &authURL))
	cmd.AddCommand(newModelsKeyCmd(&modelsURL, &authURL))
	return cmd
}

// modelsOrigin is the origin the models URL is served under, which the
// SDK's llm package builds its paths on: the URL without its llm.BasePath.
func modelsOrigin(modelsURL string) (string, bool) {
	return strings.CutSuffix(resolveModelsURL(modelsURL), llm.BasePath)
}

// modelsClient is the SDK's models client at origin, presenting key, a
// model key, unchanged, as the models capability takes it. A zero timeout
// keeps the SDK's default.
func modelsClient(origin, key string, timeout time.Duration) (*llm.Client, error) {
	cfg, err := api.Product{BaseURL: origin, Token: key, Timeout: timeout}.Config()
	if err != nil {
		return nil, err
	}
	return llm.New(cfg), nil
}

// commandModelsOrigin is the origin the model commands' calls go to, or the
// error that says the models URL names none.
func commandModelsOrigin(modelsURL string) (string, error) {
	origin, ok := modelsOrigin(modelsURL)
	if !ok {
		return "", fmt.Errorf("the models URL %s does not end in %s, the path the models are served under, as %s does", resolveModelsURL(modelsURL), llm.BasePath, defaultModelsURL)
	}
	return origin, nil
}

// ---- list ----

// modelEntry is one model of the list as --json prints it: the shape of a
// model list's entry, its id, the object it is, when it was added and who
// offers it.
type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

func newModelsListCmd(modelsURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the models your key reaches.",
		Long: `List the models your model key reaches, by the names a model call
takes.

The list carries names only. Prices per million tokens are in the
console's Models section.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsList(cmd, *modelsURL, *authURL, jsonF)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func runModelsList(cmd *cobra.Command, modelsURL, authURL string, jsonF bool) error {
	entries, _, err := listModels(cmd.Context(), modelsURL, authURL)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonF {
		return printJSON(out, entries)
	}
	if len(entries) == 0 {
		if _, err := fmt.Fprintln(out, "No models."); err != nil {
			return fmt.Errorf("write model list: %w", err)
		}
		return nil
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.ID)
		b.WriteByte('\n')
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write model list: %w", err)
	}
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "# Prices per million tokens are in the console's Models section."); err != nil {
		return fmt.Errorf("write model list note: %w", err)
	}
	return nil
}

// listModels answers the models the key reaches, and the key the core
// accepted for the list.
func listModels(ctx context.Context, modelsURL, authURL string) ([]modelEntry, string, error) {
	origin, err := commandModelsOrigin(modelsURL)
	if err != nil {
		return nil, "", err
	}
	keys, l, res, err := modelKey(ctx, modelsURL, authURL)
	if err != nil {
		return nil, "", err
	}
	var (
		models   []llm.Model
		accepted string
	)
	err = callWithModelKey(ctx, keys, l, res, func(bearer string) error {
		c, err := modelsClient(origin, bearer, modelsTimeout)
		if err != nil {
			return err
		}
		models, err = c.ListModels(ctx)
		if err == nil {
			accepted = bearer
		}
		return err
	})
	if err != nil {
		return nil, "", wrapModelsErr(err)
	}
	entries := make([]modelEntry, 0, len(models))
	for _, m := range models {
		e := modelEntry{ID: m.ID, Object: "model", OwnedBy: m.OwnedBy}
		if !m.Created.IsZero() {
			e.Created = m.Created.Unix()
		}
		entries = append(entries, e)
	}
	return entries, accepted, nil
}

// ---- env ----

func newModelsEnvCmd(modelsURL, authURL *string) *cobra.Command {
	var provider string
	var raw bool
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print shell exports that point a stock SDK at a door, with your model key.",
		Long: `Print 'export' lines that point a stock SDK at one door of the Latere
API, with your model key as the SDK's API key.

    latere models env                      # OpenAI SDK: OPENAI_BASE_URL, OPENAI_API_KEY
    latere models env --provider anthropic # Anthropic SDK: ANTHROPIC_BASE_URL, ANTHROPIC_API_KEY

--provider names the SDK's dialect, not who serves the model: both doors
reach every model in the catalog, translated when the model's provider
speaks another dialect. Name the model as the catalog does, e.g.
"openai/gpt-4.1-mini" through the Anthropic SDK. There is no Gemini
door here: it reaches no catalog model.

stdout carries the exports alone, so it is safe to eval; stderr says which
key they carry. --raw prints the bare key.`,
		Example: `  eval "$(latere models env)"
  eval "$(latere models env --provider anthropic)"
  KEY=$(latere models env --raw)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var door sdkDoor
			for _, d := range sdkDoors {
				if d.name == provider {
					door = d
				}
			}
			if reason, ok := unreachingDoors[provider]; door.name == "" && ok {
				return errors.New(reason)
			}
			if door.name == "" {
				return fmt.Errorf("unknown --provider %q; one of: %s", provider, strings.Join(sdkDoorNames(), ", "))
			}
			keys, _, res, err := modelKey(cmd.Context(), *modelsURL, *authURL)
			if err != nil {
				return err
			}
			provenance := modelKeyProvenance(res, keys.Store.Name())
			out := cmd.OutOrStdout()
			if raw {
				if _, err := fmt.Fprintln(out, res.Record.Value); err != nil {
					return err
				}
				_, err := fmt.Fprintf(cmd.ErrOrStderr(), "# %s\n", provenance)
				return err
			}
			baseValue, err := quoteShellValue(resolveModelsURL(*modelsURL) + door.suffix)
			if err != nil {
				return err
			}
			keyValue, err := quoteShellValue(res.Record.Value)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "export %s=%s\n", door.baseVar, baseValue); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "export %s=%s\n", door.keyVar, keyValue); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "# %s\n", provenance)
			return err
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "openai", "the SDK's dialect: "+strings.Join(sdkDoorNames(), ", "))
	_ = cmd.RegisterFlagCompletionFunc("provider",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return sdkDoorNames(), cobra.ShellCompDirectiveNoFileComp
		})
	cmd.Flags().BoolVar(&raw, "raw", false, "print the bare key only, no exports")
	return cmd
}

// ---- invoke ----

func newModelsInvokeCmd(modelsURL, authURL *string) *cobra.Command {
	var (
		model     string
		maxTokens int
		jsonF     bool
	)
	cmd := &cobra.Command{
		Use:   "invoke <prompt>",
		Short: "One raw model call, to check that a model answers your key.",
		Long: `Send one prompt to a model and print the reply.

This is a check, not an assistant: no tools, no session, no workspace.
Use it to see that a model answers your key. For assistant work, run
'latere agents run -p "<prompt>"'.

Name the model as 'latere models' lists it.`,
		Example: `  latere models invoke --model anthropic/claude-sonnet-5.5 "Say hi"
  latere models invoke --model openai/gpt-4.1-mini --json "Say hi"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if model == "" {
				return errors.New("--model is required")
			}
			origin, err := commandModelsOrigin(*modelsURL)
			if err != nil {
				return err
			}
			keys, l, res, err := modelKey(cmd.Context(), *modelsURL, *authURL)
			if err != nil {
				return err
			}
			limit := int64(maxTokens)
			req := &llm.Request{
				Model:     model,
				MaxTokens: &limit,
				Messages:  []llm.Message{llm.UserText(strings.Join(args, " "))},
			}
			var answer *llm.Result
			err = callWithModelKey(cmd.Context(), keys, l, res, func(bearer string) error {
				c, err := modelsClient(origin, bearer, modelsTimeout)
				if err != nil {
					return err
				}
				answer, err = c.Generate(cmd.Context(), req)
				return err
			})
			if err != nil {
				return wrapModelsErr(err)
			}
			if jsonF {
				raw, err := json.Marshal(answer.Response)
				if err != nil {
					return fmt.Errorf("render the response: %w", err)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(answer.Text()))
			return err
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "model name, as 'latere models' lists it (required)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 1024, "max output tokens")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the raw response")
	return cmd
}

// modelsRefusal is the core's refusal of a model call, worded as its code
// and sentence, or its status and text for an answer without a code.
type modelsRefusal struct{ err *latere.Error }

func (r *modelsRefusal) Error() string {
	if r.err.Code != "" {
		return r.err.Code + ": " + api.RefusalText(r.err)
	}
	return fmt.Sprintf("status %d: %s", r.err.StatusCode, api.RefusalText(r.err))
}

func (r *modelsRefusal) Unwrap() error { return r.err }

// wrapModelsErr adds what to do next to the core's refusals a person acts
// on, leaving other errors untouched.
func wrapModelsErr(err error) error {
	apiErr, ok := errors.AsType[*latere.Error](err)
	if !ok {
		return err
	}
	err = &modelsRefusal{err: apiErr}
	switch apiErr.Code {
	case "model_not_found", "model_not_allowed":
		return fmt.Errorf("%w\nRun `latere models` to see the models your key reaches", err)
	case "budget_exhausted", "spend_exceeded":
		return fmt.Errorf("%w\nThe console's Billing section shows the balance and limits of your current context", err)
	}
	return err
}
