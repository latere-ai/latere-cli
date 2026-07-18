// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"latere.ai/x/pkg/luxsdk"
	"latere.ai/x/topos/models"
	toposlux "latere.ai/x/topos/models/lux"

	"github.com/latere-ai/latere-cli/internal/api"
)

// luxDefaultModel is the Anthropic model `latere topos --local` requests through
// Lux when --model is not given. It is a current, enabled model on the Lux
// catalog (see `latere lux models`); override with --model.
const luxDefaultModel = "claude-opus-4-8"

// errNeedAuth signals that the local agent has no usable model credential, so
// the caller should run the auth picker.
var errNeedAuth = errors.New("no model credential configured")

// providerConfig is the persisted choice of model provider for the local agent.
// It is not a credential store for OAuth (that lives in claude.json); it records
// the provider and any API key / Ollama settings.
type providerConfig struct {
	Provider   string `json:"provider"`              // "anthropic" | "ollama"
	Method     string `json:"method,omitempty"`      // anthropic: "oauth" | "apikey"
	APIKey     string `json:"api_key,omitempty"`     // anthropic api key
	Model      string `json:"model,omitempty"`       // optional model override
	OllamaHost string `json:"ollama_host,omitempty"` // ollama base URL
}

func providerConfigPath() string {
	if p := os.Getenv("LATERE_TOPOS_PROVIDER_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "latere", "topos-provider.json")
}

func loadProviderConfig() (providerConfig, error) {
	var c providerConfig
	b, err := os.ReadFile(providerConfigPath())
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func saveProviderConfig(c providerConfig) error {
	p := providerConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// buildLocalModel resolves the model for `latere topos --local`. Resolution
// order: an explicit ANTHROPIC_API_KEY; a saved provider choice (the picker);
// Latere Lux when signed in to latere (the default — keyless, billed to the
// login); an ambient CLAUDE_CODE_OAUTH_TOKEN; a legacy stored Claude login.
// If nothing is available it returns errNeedAuth so the caller runs the picker.
func buildLocalModel(ctx context.Context, modelName string) (models.Model, error) {
	// 1. An explicit API key always wins.
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return anthropicDirect(key, false, modelName)
	}

	// 2. An explicit provider choice (the picker / `latere topos login`) wins over
	// the ambient defaults below.
	if cfg, err := loadProviderConfig(); err == nil && cfg.Provider != "" {
		return modelFromProviderConfig(ctx, cfg, modelName)
	}

	// 3. Latere Lux (the default once signed in to latere). Inference is routed
	// through the gateway with the user's identity, and Lux injects its own
	// upstream provider credential — so this does not share (or get rate-limited
	// alongside) a Claude Code subscription token. This is the "it just works
	// after login" path: no BYO credential, cost tracked on the login.
	if _, err := api.LoadAuthToken(); err == nil {
		return luxLocalModel(modelName), nil
	}

	// 4. The ambient Claude Code OAuth token (shared with Claude Code; this is the
	// rate-limited subscription token — only used when not signed in to latere).
	for _, env := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_AUTO"} {
		if tok := os.Getenv(env); tok != "" {
			return anthropicDirect(tok, true, modelName)
		}
	}

	// 5. A stored Claude login with no provider config (legacy).
	if tok, berr := claudeOAuthBearer(ctx); berr == nil && tok != "" {
		return anthropicDirect(tok, true, modelName)
	}
	return nil, errNeedAuth
}

// anthropicDirect builds a provider-direct Anthropic model via luxsdk:
// the lux format translated client-side, no gateway in the path. oauth
// selects bearer + OAuth-beta auth (Claude subscription tokens) over
// x-api-key.
func anthropicDirect(credential string, oauth bool, modelName string) (models.Model, error) {
	var opts []luxsdk.Option
	if oauth {
		opts = append(opts, luxsdk.WithOAuthToken())
	}
	d, err := luxsdk.NewDirect(luxsdk.ProviderAnthropic, credential, "", opts...)
	if err != nil {
		return nil, err
	}
	var lopts []toposlux.Option
	if modelName != "" {
		lopts = append(lopts, toposlux.WithModel(modelName))
	}
	return toposlux.NewFromCaller(d, lopts...), nil
}

// luxLocalModel routes inference through Latere Lux's native dialect
// (POST <lux>/lux/v1/generate, lux spec 33), authenticated with the
// caller's latere identity bearer per request (refreshed on expiry).
func luxLocalModel(modelName string) models.Model {
	model := modelName
	if model == "" {
		model = luxDefaultModel
	}
	return toposlux.New("", resolveLuxURL(""),
		toposlux.WithModel(model),
		toposlux.WithBearerSource(func(ctx context.Context) (string, error) {
			return luxIdentityBearer(ctx, "", "", "")
		}),
	)
}

// modelFromProviderConfig builds the model for an explicit provider choice.
func modelFromProviderConfig(ctx context.Context, cfg providerConfig, modelName string) (models.Model, error) {
	model := modelName
	if model == "" {
		model = cfg.Model
	}
	switch cfg.Provider {
	case "lux":
		return luxLocalModel(model), nil
	case "anthropic":
		if cfg.Method == "apikey" && cfg.APIKey != "" {
			return anthropicDirect(cfg.APIKey, false, model)
		}
		tok, err := claudeOAuthBearer(ctx)
		if err != nil {
			return nil, err
		}
		if tok == "" {
			return nil, errNeedAuth
		}
		return anthropicDirect(tok, true, model)
	case "ollama":
		host := cfg.OllamaHost
		if host == "" {
			host = "http://localhost:11434"
		}
		if model == "" {
			model = "llama3.1" // the old adapter's default tool-capable model
		}
		d, err := luxsdk.NewDirect(luxsdk.ProviderOllama, "", host)
		if err != nil {
			return nil, err
		}
		return toposlux.NewFromCaller(d, toposlux.WithModel(model)), nil
	default:
		return nil, errNeedAuth
	}
}
