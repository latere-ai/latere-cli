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
	"strings"

	"latere.ai/x/topos/models"
	"latere.ai/x/topos/models/anthropic"
	"latere.ai/x/topos/models/ollama"

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
	anthOpts := func() []anthropic.Option {
		if modelName != "" {
			return []anthropic.Option{anthropic.WithModel(modelName)}
		}
		return nil
	}

	// 1. An explicit API key always wins.
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return anthropic.New(key, "", anthOpts()...), nil
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
			return anthropic.New(tok, "", append(anthOpts(), anthropic.WithOAuthToken())...), nil
		}
	}

	// 5. A stored Claude login with no provider config (legacy).
	if tok, berr := claudeOAuthBearer(ctx); berr == nil && tok != "" {
		return anthropic.New(tok, "", append(anthOpts(), anthropic.WithOAuthToken())...), nil
	}
	return nil, errNeedAuth
}

// luxLocalModel builds the Anthropic adapter pointed at Latere Lux's Anthropic
// proxy (<lux>/anthropic, the adapter appends /v1/messages), authenticated with
// the caller's latere identity bearer per request (refreshed on expiry). This is
// the same wiring the hosted brain uses (agents/internal/runtime/brain).
func luxLocalModel(modelName string) models.Model {
	base := strings.TrimRight(resolveLuxURL(""), "/") + "/anthropic"
	model := modelName
	if model == "" {
		model = luxDefaultModel
	}
	return anthropic.New("", base,
		anthropic.WithModel(model),
		anthropic.WithBearerSource(func(ctx context.Context) (string, error) {
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
			var opts []anthropic.Option
			if model != "" {
				opts = append(opts, anthropic.WithModel(model))
			}
			return anthropic.New(cfg.APIKey, "", opts...), nil
		}
		tok, err := claudeOAuthBearer(ctx)
		if err != nil {
			return nil, err
		}
		if tok == "" {
			return nil, errNeedAuth
		}
		opts := []anthropic.Option{anthropic.WithOAuthToken()}
		if model != "" {
			opts = append(opts, anthropic.WithModel(model))
		}
		return anthropic.New(tok, "", opts...), nil
	case "ollama":
		host := cfg.OllamaHost
		if host == "" {
			host = "http://localhost:11434"
		}
		return ollama.New(host, model), nil
	default:
		return nil, errNeedAuth
	}
}
