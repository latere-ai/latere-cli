// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"latere.ai/x/pkg/otel"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The model commands reach the Lux core behind the platform origin, at
// https://api.latere.ai/v1/models. The core serves one door per wire
// dialect under that base, and every door reaches every Model in the
// catalog. The credential of every call is the model key (models_key.go).

// defaultModelsURL is the base of the core's doors at the origin.
const defaultModelsURL = "https://api.latere.ai/v1/models"

// defaultCatalogModel is the Model review's critics and the local Topos
// agent call when none is named. Models are named as the catalog names
// them, the provider in front.
const defaultCatalogModel = "anthropic/claude-sonnet-4.6"

// openAIDoor is the core's OpenAI door under the base. The CLI's own model
// calls go through it: it serves the model list and Chat Completions, and a
// Model of any provider is translated behind it.
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
// Anthropic and Gemini SDKs append their own version segment).
type sdkDoor struct {
	name, baseVar, keyVar, suffix string
}

// sdkDoors are the doors `models env` exports, in the order it names them.
// The Anthropic key goes in ANTHROPIC_API_KEY, which the SDK sends as
// x-api-key; the core accepts the key in x-api-key, x-goog-api-key or
// Authorization. GOOGLE_GEMINI_BASE_URL is read by the Gemini JavaScript
// SDK and the Gemini CLI.
var sdkDoors = []sdkDoor{
	{name: "openai", baseVar: "OPENAI_BASE_URL", keyVar: "OPENAI_API_KEY", suffix: openAIDoor + "/v1"},
	{name: "anthropic", baseVar: "ANTHROPIC_BASE_URL", keyVar: "ANTHROPIC_API_KEY", suffix: "/anthropic"},
	{name: "gemini", baseVar: "GOOGLE_GEMINI_BASE_URL", keyVar: "GEMINI_API_KEY", suffix: "/gemini"},
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
anthropic/claude-sonnet-4.6, openai/gpt-4.1-mini.

The base URL defaults to https://api.latere.ai/v1/models; LATERE_MODELS_URL
or --models-url overrides it.`,
		Example: `  latere models
  eval "$(latere models env)"
  latere models invoke --model anthropic/claude-sonnet-4.6 "Say hi"
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

// ---- list ----

// modelEntry is one row of the OpenAI door's model list.
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
takes. The list is the OpenAI door's, GET /v1/models/openai/v1/models.

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
	keys, l, res, err := modelKey(ctx, modelsURL, authURL)
	if err != nil {
		return nil, "", err
	}
	listURL := resolveModelsURL(modelsURL) + openAIDoor + "/v1/models"
	var accepted string
	raw, err := callWithModelKey(ctx, keys, l, res, func(bearer string) ([]byte, error) {
		body, err := modelsRequest(ctx, http.MethodGet, listURL, bearer, nil)
		if err == nil {
			accepted = bearer
		}
		return body, err
	})
	if err != nil {
		return nil, "", wrapModelsErr(err)
	}
	var list struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, "", fmt.Errorf("parse the model list: %w", err)
	}
	if list.Data == nil {
		list.Data = []modelEntry{}
	}
	return list.Data, accepted, nil
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
    latere models env --provider gemini    # Gemini SDK: GOOGLE_GEMINI_BASE_URL, GEMINI_API_KEY

--provider names the SDK's dialect, not who serves the model: every door
reaches every model in the catalog, translated when the model's provider
speaks another dialect. Name the model as the catalog does, e.g.
"openai/gpt-4.1-mini" through the Anthropic SDK.

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
		Long: `Send one prompt to a model through the OpenAI door and print the reply.

This is a check, not an assistant: no tools, no session, no workspace.
Use it to see that a model answers your key. For assistant work, run
'latere topos --local -p "<prompt>"'.

Name the model as 'latere models' lists it.`,
		Example: `  latere models invoke --model anthropic/claude-sonnet-4.6 "Say hi"
  latere models invoke --model openai/gpt-4.1-mini --json "Say hi"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if model == "" {
				return errors.New("--model is required")
			}
			keys, l, res, err := modelKey(cmd.Context(), *modelsURL, *authURL)
			if err != nil {
				return err
			}
			chatURL := resolveModelsURL(*modelsURL) + openAIDoor + "/v1/chat/completions"
			body := map[string]any{
				"model":      model,
				"max_tokens": maxTokens,
				"messages":   []map[string]any{{"role": "user", "content": strings.Join(args, " ")}},
			}
			raw, err := callWithModelKey(cmd.Context(), keys, l, res, func(bearer string) ([]byte, error) {
				return modelsRequest(cmd.Context(), http.MethodPost, chatURL, bearer, body)
			})
			if err != nil {
				return wrapModelsErr(err)
			}
			if jsonF {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(string(raw)))
				return err
			}
			text, err := extractChatText(raw)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), text)
			return err
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "model name, as 'latere models' lists it (required)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 1024, "max output tokens")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the raw Chat Completions response")
	return cmd
}

// extractChatText pulls the assistant text out of a Chat Completions
// response.
func extractChatText(raw []byte) (string, error) {
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

// ---- transport ----

// maxModelsResponse bounds a model response the CLI reads whole.
const maxModelsResponse = 8 << 20

// modelsRequest sends one request to a door with the bearer, a JSON body
// when body is non-nil, and returns the whole response body. A non-2xx
// answer becomes an *api.APIError carrying the door's code and message, so
// callWithModelKey can see a 401 and wrapModelsErr can explain a refusal.
func modelsRequest(ctx context.Context, method, url, bearer string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", "latere-cli")
	resp, err := (&http.Client{Timeout: 120 * time.Second, Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponse+1))
	if resp.StatusCode/100 != 2 {
		// Keep HTTP failures structured, with bounded diagnostic text.
		if len(respBody) > maxModelsResponse {
			respBody = respBody[:maxModelsResponse]
		}
		e := &api.APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(respBody))}
		_ = json.Unmarshal(respBody, e)
		if e.Code == "" {
			// The doors answer in their dialect's envelope,
			// {"error":{"type","code","message"}}; without this the caller
			// sees the raw JSON instead of the code and the sentence.
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
		return nil, fmt.Errorf("read model response: %w", readErr)
	}
	if len(respBody) > maxModelsResponse {
		return nil, errors.New("model response exceeds 8 MiB limit")
	}
	return respBody, nil
}

// wrapModelsErr adds what to do next to the core's refusals a person acts
// on, leaving other errors untouched.
func wrapModelsErr(err error) error {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case "model_not_found", "model_not_allowed":
		return fmt.Errorf("%w\nRun `latere models` to see the models your key reaches", err)
	case "budget_exhausted", "spend_exceeded":
		return fmt.Errorf("%w\nThe console's Billing section shows the balance and limits of your current context", err)
	}
	return err
}
