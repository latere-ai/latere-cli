// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"latere.ai/x/pkg/llmdialect/ir"
	"latere.ai/x/topos/models"
	"latere.ai/x/topos/models/dialect"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/modelkey"
)

// errNeedAuth signals that the local agent has no usable model credential, so
// the caller should run the auth picker.
var errNeedAuth = errors.New("no model credential configured")

// providerConfig is the persisted choice of model provider for the local
// agent: the provider and any API key / Ollama settings. A Claude sign-in
// (anthropic with method oauth) was a choice of earlier versions, which is
// no longer a provider; a file that holds one is refused with the command
// that chooses again.
type providerConfig struct {
	Provider   string `json:"provider"`              // "anthropic" | "ollama" | "lux"
	Method     string `json:"method,omitempty"`      // anthropic: "apikey"
	APIKey     string `json:"api_key,omitempty"`     // anthropic api key
	Model      string `json:"model,omitempty"`       // optional model override
	OllamaHost string `json:"ollama_host,omitempty"` // ollama base URL
}

// providerFile is where the local agent's provider choice is kept, in the
// user's configuration directory. legacyProviderFile is the name it had
// before spec 011; a choice found there is moved, not chosen again.
const (
	providerFile       = "agent-provider.json"
	legacyProviderFile = "topos-provider.json"
)

// providerConfigPath is $LATERE_AGENT_PROVIDER_FILE, or providerFile in the
// user's configuration directory.
func providerConfigPath() string {
	if p := os.Getenv(envAgentProviderFile); p != "" {
		return p
	}
	return filepath.Join(latereConfigDir(), providerFile)
}

func latereConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "latere")
}

// loadProviderConfig reads the provider choice. A choice saved under the
// legacy name, with none under the current one and no variable naming the
// file, is moved to the current name first.
func loadProviderConfig() (providerConfig, error) {
	var c providerConfig
	p := providerConfigPath()
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) && os.Getenv(envAgentProviderFile) == "" {
		legacy := filepath.Join(latereConfigDir(), legacyProviderFile)
		if _, statErr := os.Stat(legacy); statErr == nil {
			if mvErr := os.Rename(legacy, p); mvErr != nil {
				return c, fmt.Errorf("move the provider choice from %s to %s: %w", legacy, p, mvErr)
			}
			b, err = os.ReadFile(p)
		}
	}
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

// localModel is the model a local session runs on: its name as the session
// shows it, and connect, which answers the model a turn streams through,
// where its requests go and the model's figures. connect is asked at each
// turn, so choosing a model reaches nothing until a turn runs on it.
type localModel struct {
	name    string
	connect func(ctx context.Context) (models.Model, models.Connection, models.Entry, error)
}

// localDialect is the model over HTTP every local turn streams through,
// with the CLI's traced client.
var localDialect = &dialect.Model{Client: api.HTTPClient()}

// The direct Anthropic connection: Anthropic's API, and the model a session
// runs when none is named.
const (
	anthropicURL          = "https://api.anthropic.com"
	defaultAnthropicModel = "claude-opus-4-8"
)

// errRetiredProvider is a saved Claude sign-in, which is no longer a
// provider.
var errRetiredProvider = errors.New("the saved provider is a Claude sign-in, which is no longer a provider; use an Anthropic API key, chosen with `latere agents provider` or set as ANTHROPIC_API_KEY, or the Latere API after `latere login`")

// buildLocalModel resolves the model for `latere agents run`. Resolution
// order: an explicit ANTHROPIC_API_KEY; a saved provider choice (the
// picker); the Latere models at the origin when signed in to latere or
// handed a model key (the default, billed to the key's context). If
// nothing is available it returns errNeedAuth so the caller runs the
// picker.
func buildLocalModel(ctx context.Context, modelName string) (localModel, error) {
	// 1. An explicit API key always wins.
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return anthropicDirect(key, modelName), nil
	}

	// 2. An explicit provider choice (the picker / `latere agents provider`)
	// wins over the default below.
	cfg, err := loadProviderConfig()
	if err == nil && cfg.Provider != "" {
		return modelFromProviderConfig(cfg, modelName)
	}

	// 3. The Latere models at the origin (the default once signed in to
	// latere). The call presents the model key and the core holds the
	// upstream provider credentials, so no provider credential is on this
	// machine and the cost is drawn from the key's context.
	_, handedKey := modelkey.FromEnv()
	if _, err := api.LoadAuthToken(); err == nil || handedKey {
		return originLocalModel(modelName), nil
	}
	return localModel{}, errNeedAuth
}

// anthropicDirect is a model at Anthropic's API, called with key: the
// requests are Anthropic's Messages, with no gateway in the path.
func anthropicDirect(key, modelName string) localModel {
	name := cmp.Or(modelName, defaultAnthropicModel)
	return localModel{name: name, connect: func(context.Context) (models.Model, models.Connection, models.Entry, error) {
		entry, err := catalogEntry(name)
		if err != nil {
			return nil, models.Connection{}, models.Entry{}, err
		}
		return localDialect, models.Connection{
			BaseURL: anthropicURL, Model: name, Family: models.FamilyAnthropic,
			Dialect: ir.DialectAnthropicMessages, Credential: key,
		}, entry, nil
	}}
}

// catalogEntry is the figures of a model the catalog built into topos
// names; a model it does not know is refused.
func catalogEntry(name string) (models.Entry, error) {
	cat, err := models.Embedded()
	if err != nil {
		return models.Entry{}, err
	}
	return cat.Resolve(name)
}

// originLocalModel calls models through the core at the origin, which the
// models URL names as a Lux root: each request goes to the door of its
// model's family, with the model key, which modelKeySource has the core
// accept before the first call, and the figures are the catalog's overlaid
// by those the door lists for the key.
func originLocalModel(modelName string) localModel {
	name := cmp.Or(modelName, defaultCatalogModel)
	key := modelKeySource("", "")
	base := resolveModelsURL("")
	var (
		mu    sync.Mutex
		doors models.Doors
		found bool
	)
	return localModel{name: name, connect: func(ctx context.Context) (models.Model, models.Connection, models.Entry, error) {
		credential, err := key(ctx)
		if err != nil {
			return nil, models.Connection{}, models.Entry{}, err
		}
		mu.Lock()
		if !found {
			if doors, err = dialect.Discover(ctx, api.HTTPClient(), base); err == nil {
				found = true
			}
		}
		at := doors
		mu.Unlock()
		if err != nil {
			return nil, models.Connection{}, models.Entry{}, fmt.Errorf("read the models at %s: %w", base, err)
		}
		cat, err := models.Embedded()
		if err != nil {
			return nil, models.Connection{}, models.Entry{}, err
		}
		first := cat.Overlay(name)
		conn := models.Connection{BaseURL: base, Model: name, Family: first.Family, Dialect: first.Dialect, Credential: credential}
		conn.BaseURL = at.Door(base, conn.EffectiveDialect())
		var served models.Entry
		if models.NamesADoor(conn.BaseURL) {
			if served, err = dialect.Served(ctx, api.HTTPClient(), conn); err != nil {
				return nil, models.Connection{}, models.Entry{}, err
			}
		}
		entry, err := cat.Resolve(name, served)
		if err != nil {
			return nil, models.Connection{}, models.Entry{}, err
		}
		conn.Family, conn.Dialect = entry.Family, entry.Dialect
		return localDialect, conn, entry, nil
	}}
}

// modelFromProviderConfig builds the model for an explicit provider choice.
func modelFromProviderConfig(cfg providerConfig, modelName string) (localModel, error) {
	model := cmp.Or(modelName, cfg.Model)
	switch {
	case cfg.Provider == "lux":
		return originLocalModel(model), nil
	case cfg.Provider == "anthropic" && cfg.Method == "apikey" && cfg.APIKey != "":
		return anthropicDirect(cfg.APIKey, model), nil
	case cfg.Provider == "anthropic" && cfg.Method == "oauth":
		return localModel{}, errRetiredProvider
	case cfg.Provider == "ollama":
		return ollamaModel(cmp.Or(cfg.OllamaHost, defaultOllamaHost), cmp.Or(model, defaultOllamaModel)), nil
	}
	return localModel{}, errNeedAuth
}

// The Ollama connection: the host it serves on, the model a session runs
// when none is named, and the output limit of every request, the one the
// agent's earlier runtime asked of Ollama, since Ollama answers none.
const (
	defaultOllamaHost  = "http://localhost:11434"
	defaultOllamaModel = "llama3.1"
	ollamaMaxOutput    = 8192
)

// maxOllamaShow bounds Ollama's answer about a model, which without
// verbose names its parameters, template and metadata.
const maxOllamaShow = 4 << 20

// ollamaModel is a model Ollama serves at host, called through its OpenAI
// Chat Completions endpoint with no credential. The input window is the
// context length Ollama reports for the model, read once at the session's
// first turn; a model it reports none for is refused, since the agent
// needs one to know when to compact the context.
func ollamaModel(host, name string) localModel {
	host = strings.TrimRight(host, "/")
	var (
		mu     sync.Mutex
		window int64
	)
	return localModel{name: name, connect: func(ctx context.Context) (models.Model, models.Connection, models.Entry, error) {
		mu.Lock()
		defer mu.Unlock()
		if window == 0 {
			w, err := ollamaContextLength(ctx, host, name)
			if err != nil {
				return nil, models.Connection{}, models.Entry{}, err
			}
			window = w
		}
		return localDialect, models.Connection{
			BaseURL: host, Model: name, Family: models.FamilyOther, Dialect: ir.DialectOpenAIChat,
		}, models.Entry{
			Name: name, Family: models.FamilyOther, Dialect: ir.DialectOpenAIChat,
			InputWindow: window, MaxOutputTokens: ollamaMaxOutput,
		}, nil
	}}
}

// ollamaContextLength is the context length Ollama reports for model at
// host: the "<architecture>.context_length" of its POST /api/show answer.
func ollamaContextLength(ctx context.Context, host, model string) (int64, error) {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/show", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := api.HTTPClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("reach Ollama at %s: %w", host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOllamaShow))
	if err != nil {
		return 0, fmt.Errorf("read Ollama's answer about %s: %w", model, err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the Ollama at %s answered %d about %s: %s", host, resp.StatusCode, model, truncLine(string(raw), 200))
	}
	var show struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal(raw, &show); err != nil {
		return 0, fmt.Errorf("read Ollama's answer about %s: %w", model, err)
	}
	arch, _ := show.ModelInfo["general.architecture"].(string)
	length, _ := show.ModelInfo[arch+".context_length"].(float64)
	if arch == "" || length < 1 {
		return 0, fmt.Errorf("the Ollama at %s reports no context length for %s, and the agent needs one to know when to compact the conversation; choose another model with /model or --model", host, model)
	}
	return int64(length), nil
}
