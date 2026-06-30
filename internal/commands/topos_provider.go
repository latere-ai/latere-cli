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

	"latere.ai/x/topos/models"
	"latere.ai/x/topos/models/anthropic"
	"latere.ai/x/topos/models/ollama"
)

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
// order: explicit env (ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN[_AUTO]), then
// the saved provider config (Anthropic OAuth login / API key, or Ollama). If
// nothing is configured it returns errNeedAuth so the caller runs the picker.
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
	// the AMBIENT CLAUDE_CODE_OAUTH_TOKEN env, so a user who picked Ollama or an
	// API key to escape Claude Code's shared rate limit actually gets it.
	if cfg, err := loadProviderConfig(); err == nil && cfg.Provider != "" {
		return modelFromProviderConfig(ctx, cfg, modelName)
	}

	// 3. The ambient Claude Code OAuth token (shared with Claude Code; note this
	// is the rate-limited subscription token).
	for _, env := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_AUTO"} {
		if tok := os.Getenv(env); tok != "" {
			return anthropic.New(tok, "", append(anthOpts(), anthropic.WithOAuthToken())...), nil
		}
	}

	// 4. A stored Claude login with no provider config (legacy).
	if tok, berr := claudeOAuthBearer(ctx); berr == nil && tok != "" {
		return anthropic.New(tok, "", append(anthOpts(), anthropic.WithOAuthToken())...), nil
	}
	return nil, errNeedAuth
}

// modelFromProviderConfig builds the model for an explicit provider choice.
func modelFromProviderConfig(ctx context.Context, cfg providerConfig, modelName string) (models.Model, error) {
	model := modelName
	if model == "" {
		model = cfg.Model
	}
	switch cfg.Provider {
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
