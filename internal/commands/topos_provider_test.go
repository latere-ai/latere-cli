// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBuildLocalModelFromProviderConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_AUTO", "")
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))
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

// TestProviderConfigBeatsAmbientClaudeToken is the 429 fix: a user who picked a
// provider (Ollama / API key) to escape Claude Code's shared, rate-limited token
// must get that provider even when CLAUDE_CODE_OAUTH_TOKEN is set in their env.
func TestProviderConfigBeatsAmbientClaudeToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-ambient-shared")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_AUTO", "")
	t.Setenv("LATERE_CLAUDE_TOKEN_FILE", filepath.Join(t.TempDir(), "claude.json"))
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))

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
	if got := authModel(newAuthModel()).updated(enter).result; got.choice != authClaude {
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
