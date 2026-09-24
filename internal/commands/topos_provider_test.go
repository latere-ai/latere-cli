// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"latere.ai/x/topos/models"
	toposlux "latere.ai/x/topos/models/lux"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestBuildLocalModelFromProviderConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_AUTO", "")
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))
	cfgPath := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", cfgPath)

	// No config + no env → errNeedAuth (so --local shows the picker).
	if _, err := buildLocalModel(context.Background(), ""); !errors.Is(err, errNeedAuth) {
		t.Fatalf("no config err = %v, want errNeedAuth", err)
	}

	// Anthropic API key config → a model.
	if err := saveProviderConfig(providerConfig{Provider: "anthropic", Method: "apikey", APIKey: "sk-ant-api-x"}); err != nil {
		t.Fatal(err)
	}
	if m, err := buildLocalModel(context.Background(), ""); err != nil || m == nil {
		t.Fatalf("apikey config = (%v, %v)", m, err)
	}

	// Ollama config → a model (no credential needed).
	if err := saveProviderConfig(providerConfig{Provider: "ollama", Model: "llama3"}); err != nil {
		t.Fatal(err)
	}
	if m, err := buildLocalModel(context.Background(), ""); err != nil || m == nil {
		t.Fatalf("ollama config = (%v, %v)", m, err)
	}
}

// TestBuildLocalModelDefaultsToTheOrigin is the real 429 fix: once signed in
// to latere, --local calls the Latere models at the origin (billed to the
// model key's context) instead of the shared, rate-limited
// CLAUDE_CODE_OAUTH_TOKEN. The origin must win over that ambient token; we
// tell them apart by the requested model id (the origin path pins
// defaultCatalogModel, the ambient path uses the adapter's own default).
func TestBuildLocalModelDefaultsToTheOrigin(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-ambient-shared")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_AUTO", "")
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))

	// Signed in to latere → the origin is the default, overriding the ambient token.
	if err := api.SaveAuthToken(api.Token{AccessToken: "latere-access-token"}); err != nil {
		t.Fatal(err)
	}
	m, err := buildLocalModel(context.Background(), "")
	if err != nil || m == nil {
		t.Fatalf("signed in → origin model, got (%v, %v)", m, err)
	}
	if got := m.(*toposlux.Adapter).Model(); got != defaultCatalogModel {
		t.Fatalf("model = %q, want the origin default %q (the origin did not win over the ambient token)", got, defaultCatalogModel)
	}
	// An explicit --model is honored on the origin path.
	if m, _ := buildLocalModel(context.Background(), "anthropic/claude-haiku-4.5"); m.(*toposlux.Adapter).Model() != "anthropic/claude-haiku-4.5" {
		t.Fatalf("--model override not applied on the origin path")
	}
}

// TestLocalModelCallsTheDoor is criterion 4 for the local Topos model path:
// the agent's model call reaches the core's OpenAI door with the model key
// and the catalog name of the default model.
func TestLocalModelCallsTheDoor(t *testing.T) {
	w := newKeyWorld(t, "")
	t.Setenv("LATERE_MODELS_URL", w.modelsURL())
	t.Setenv("AUTH_URL", w.srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	m, err := buildLocalModel(t.Context(), "")
	if err != nil {
		t.Fatalf("buildLocalModel: %v", err)
	}
	st, err := m.Stream(t.Context(), models.Request{Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var text strings.Builder
	for {
		ev, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		text.WriteString(ev.TextDelta)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if text.String() != "no findings" {
		t.Fatalf("reply = %q", text.String())
	}
	calls := w.coreCalls()
	if len(calls) != 2 || calls[0].path != "/v1/models/openai/v1/models" ||
		calls[1].path != "/v1/models/openai/v1/chat/completions" {
		t.Fatalf("core calls = %+v, want the key's list then the chat", calls)
	}
	if calls[1].bearer != "pat_k1.value" || calls[1].model != defaultCatalogModel {
		t.Fatalf("chat = %+v, want the model key and %s", calls[1], defaultCatalogModel)
	}
}

// TestProviderConfigBeatsAmbientClaudeToken is the 429 fix: a user who picked a
// provider (Ollama / API key) to escape Claude Code's shared, rate-limited token
// must get that provider even when CLAUDE_CODE_OAUTH_TOKEN is set in their env.
func TestProviderConfigBeatsAmbientClaudeToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-ambient-shared")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_AUTO", "")
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	// No latere/Lux token, so the ambient-token fallback path is exercised.
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))

	// The explicit Ollama choice must win over the ambient Claude token.
	if err := saveProviderConfig(providerConfig{Provider: "ollama", Model: "llama3"}); err != nil {
		t.Fatal(err)
	}
	if m, err := buildLocalModel(context.Background(), ""); err != nil || m == nil {
		t.Fatalf("config should override ambient token, got (%v, %v)", m, err)
	}

	// With no provider config, the ambient token is the fallback (still usable).
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "empty.json"))
	if m, err := buildLocalModel(context.Background(), ""); err != nil || m == nil {
		t.Fatalf("ambient token fallback = (%v, %v)", m, err)
	}
}

func TestProviderConfigRoundTrip(t *testing.T) {
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "p.json"))
	want := providerConfig{Provider: "anthropic", Method: "apikey", APIKey: "k", Model: "m"}
	if err := saveProviderConfig(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadProviderConfig()
	if err != nil || got != want {
		t.Fatalf("load = (%+v, %v), want %+v", got, err, want)
	}
}

func TestAuthPickerSelections(t *testing.T) {
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	// Cursor 0 = Claude → authClaude.
	if got := newAuthModel().updated(enter).result; got.choice != authClaude {
		t.Fatalf("first option = %v, want authClaude", got.choice)
	}

	// Down to API key, Enter → entering; type, Enter → authAPIKey + value.
	m := newAuthModel()
	m = m.updated(tea.KeyMsg{Type: tea.KeyDown})
	m = m.updated(enter)
	if !m.entering {
		t.Fatal("selecting API key should enter text-input mode")
	}
	m.input.SetValue("sk-ant-api-typed")
	m = m.updated(enter)
	if m.result.choice != authAPIKey || m.result.apiKey != "sk-ant-api-typed" {
		t.Fatalf("api key result = %+v", m.result)
	}

	// Down twice to Ollama → authOllama.
	m = newAuthModel()
	m = m.updated(tea.KeyMsg{Type: tea.KeyDown}).updated(tea.KeyMsg{Type: tea.KeyDown}).updated(enter)
	if m.result.choice != authOllama {
		t.Fatalf("third option = %v, want authOllama", m.result.choice)
	}

	// q cancels.
	if got := newAuthModel().updated(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}).result; got.choice != authCancel {
		t.Fatalf("q = %v, want authCancel", got.choice)
	}
}

// updated is a test helper: apply one message and return the concrete model.
func (m authModel) updated(msg tea.Msg) authModel {
	next, _ := m.Update(msg)
	return next.(authModel)
}
