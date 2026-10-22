// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestBuildLocalModelFromProviderConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))
	cfgPath := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", cfgPath)

	// No config + no env → errNeedAuth (so the command shows the picker).
	if _, err := buildLocalModel(context.Background(), ""); !errors.Is(err, errNeedAuth) {
		t.Fatalf("no config err = %v, want errNeedAuth", err)
	}

	// Anthropic API key config → a direct model with that key.
	if err := saveProviderConfig(providerConfig{Provider: "anthropic", Method: "apikey", APIKey: "sk-ant-api-x"}); err != nil {
		t.Fatal(err)
	}
	m, err := buildLocalModel(context.Background(), "")
	if err != nil {
		t.Fatalf("apikey config: %v", err)
	}
	if _, conn, _, err := m.connect(t.Context()); err != nil || conn.Credential != "sk-ant-api-x" || conn.BaseURL != anthropicURL {
		t.Fatalf("apikey config connects to %+v, %v", conn, err)
	}

	// Ollama config → a model (no credential needed), the saved model.
	if err := saveProviderConfig(providerConfig{Provider: "ollama", Model: "llama3"}); err != nil {
		t.Fatal(err)
	}
	if m, err := buildLocalModel(context.Background(), ""); err != nil || m.name != "llama3" {
		t.Fatalf("ollama config = (%q, %v)", m.name, err)
	}

	// A Claude sign-in, chosen by an earlier version, is refused with the
	// command that chooses again.
	if err := saveProviderConfig(providerConfig{Provider: "anthropic", Method: "oauth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildLocalModel(context.Background(), ""); !errors.Is(err, errRetiredProvider) || !strings.Contains(err.Error(), "latere agents provider") {
		t.Errorf("a saved Claude sign-in: err = %v, want the refusal naming latere agents provider", err)
	}
}

// ollamaStub is Ollama for the local agent: the model's metadata at
// /api/show, with a context length for every model but tiny, and a streamed
// Chat Completion at /v1/chat/completions. Each request is recorded.
type ollamaStub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []string
	chats    []string
}

func newOllamaStub(t *testing.T) *ollamaStub {
	t.Helper()
	o := &ollamaStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode /api/show: %v", err)
		}
		o.record("show " + body.Model)
		info := map[string]any{"general.architecture": "llama", "llama.context_length": 131072}
		if body.Model == "tiny" {
			info = map[string]any{"general.architecture": "tiny"}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"model_info": info}); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		o.record("chat")
		o.mu.Lock()
		o.chats = append(o.chats, string(raw))
		o.mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Ollama was sent a credential: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"c1","object":"chat.completion.chunk","model":"llama3.1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello from ollama"},"finish_reason":null}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"llama3.1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4}}`,
			`[DONE]`,
		} {
			if _, err := io.WriteString(w, "data: "+chunk+"\n\n"); err != nil {
				t.Error(err)
			}
		}
	})
	o.srv = httptest.NewServer(mux)
	t.Cleanup(o.srv.Close)
	return o
}

func (o *ollamaStub) record(r string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, r)
}

func (o *ollamaStub) seen() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.requests...)
}

// A saved Ollama choice runs the agent on Ollama's OpenAI endpoint: the
// first turn reads the model's context length, every turn streams its chat
// with the output limit the agent asks of Ollama, and no credential is sent.
func TestOllamaRunsTheAgent(t *testing.T) {
	o := newOllamaStub(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	if err := saveProviderConfig(providerConfig{Provider: "ollama", OllamaHost: o.srv.URL}); err != nil {
		t.Fatal(err)
	}
	m, err := buildLocalModel(t.Context(), "")
	if err != nil || m.name != defaultOllamaModel {
		t.Fatalf("buildLocalModel = (%q, %v), want %s", m.name, err, defaultOllamaModel)
	}
	rec := &recorder{}
	agent, err := newLocalAgent(t.Context(), t.TempDir(), m, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	for _, prompt := range []string{"hi", "again"} {
		if err := agent.turn(t.Context(), prompt); err != nil {
			t.Fatalf("turn %q: %v", prompt, err)
		}
	}
	var text strings.Builder
	for _, e := range rec.all() {
		if e.kind == localText {
			text.WriteString(e.text)
		}
	}
	if text.String() != "hello from ollamahello from ollama" {
		t.Errorf("streamed text = %q, want Ollama's answer to each turn", text.String())
	}
	if got := strings.Join(o.seen(), ","); got != "show llama3.1,chat,chat" {
		t.Errorf("Ollama was asked %s, want the model's figures once, then a chat per turn", got)
	}
	o.mu.Lock()
	chat := o.chats[0]
	o.mu.Unlock()
	if !strings.Contains(chat, `"model":"llama3.1"`) || !strings.Contains(chat, strconv.Itoa(ollamaMaxOutput)) {
		t.Errorf("the chat request %s does not name the model and the output limit", chat)
	}
}

// A model Ollama reports no context length for is refused at the turn,
// before any chat, with a sentence that says why and what to do.
func TestOllamaModelWithNoContextLength(t *testing.T) {
	o := newOllamaStub(t)
	agent, err := newLocalAgent(t.Context(), t.TempDir(), ollamaModel(o.srv.URL+"/", "tiny"), func(localEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	err = agent.turn(t.Context(), "hi")
	if err == nil || !strings.Contains(err.Error(), "reports no context length for tiny") || !strings.Contains(err.Error(), "/model") {
		t.Fatalf("turn = %v, want the refusal for a model with no context length", err)
	}
	if got := strings.Join(o.seen(), ","); got != "show tiny" {
		t.Errorf("Ollama was asked %s, want only the model's figures", got)
	}
}

// TestBuildLocalModelDefaultsToTheOrigin: once signed in to latere, the
// agent calls the Latere models at the origin, billed to the model key's
// context, with the catalog default unless a model is named. Building it
// reaches nothing; the first turn does.
func TestBuildLocalModelDefaultsToTheOrigin(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))

	if err := api.SaveAuthToken(api.Token{AccessToken: "latere-access-token"}); err != nil {
		t.Fatal(err)
	}
	m, err := buildLocalModel(context.Background(), "")
	if err != nil || m.name != defaultCatalogModel {
		t.Fatalf("signed in → origin model, got (%q, %v), want %s", m.name, err, defaultCatalogModel)
	}
	// An explicit --model is honored on the origin path.
	if m, _ := buildLocalModel(context.Background(), "anthropic/claude-haiku-4.5"); m.name != "anthropic/claude-haiku-4.5" {
		t.Fatalf("--model override not applied on the origin path: %q", m.name)
	}
}

// TestLocalModelCallsTheDoor is criterion 4 for the local agent's model
// path: a turn finds the core's doors at the origin, reads the model's
// figures from the door of its family with the model key, and sends its
// request there, with the key and the catalog name of the default model.
func TestLocalModelCallsTheDoor(t *testing.T) {
	w := newKeyWorld(t, "")
	t.Setenv("LATERE_MODELS_URL", w.modelsURL())
	t.Setenv("AUTH_URL", w.srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	m, err := buildLocalModel(t.Context(), "")
	if err != nil {
		t.Fatalf("buildLocalModel: %v", err)
	}
	rec := &recorder{}
	agent, err := newLocalAgent(t.Context(), t.TempDir(), m, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := agent.turn(t.Context(), "hi"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	var text strings.Builder
	for _, e := range rec.all() {
		if e.kind == localText {
			text.WriteString(e.text)
		}
	}
	if text.String() != "no findings" {
		t.Fatalf("reply = %q", text.String())
	}
	var paths []string
	for _, c := range w.coreCalls() {
		paths = append(paths, c.path)
		if c.bearer != "pat_k1.value" {
			t.Errorf("%s presented %q, want the model key", c.path, c.bearer)
		}
		if c.path == "/v1/models/anthropic/v1/messages" && (c.model != defaultCatalogModel || !c.stream) {
			t.Errorf("messages call = %+v, want a stream of %s", c, defaultCatalogModel)
		}
	}
	want := "/v1/models/lux/v1/models,/v1/models/anthropic/v1/models,/v1/models/anthropic/v1/messages"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("core calls = %s, want the key's list, the door's figures, then the request", got)
	}
}

func TestProviderConfigRoundTrip(t *testing.T) {
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "p.json"))
	want := providerConfig{Provider: "anthropic", Method: "apikey", APIKey: "k", Model: "m"}
	if err := saveProviderConfig(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadProviderConfig()
	if err != nil || got != want {
		t.Fatalf("load = (%+v, %v), want %+v", got, err, want)
	}
}

// A provider choice saved under the name it had before spec 011 is moved to
// the current name the first time it is read, so no one chooses again. With
// the variable naming a file, the legacy file is left alone.
func TestProviderConfigMovesFromLegacyName(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("HOME", config)
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", "")
	dir := latereConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "topos-provider.json")
	if err := os.WriteFile(legacy, []byte(`{"provider":"ollama","model":"llama3"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadProviderConfig()
	if err != nil || got.Provider != "ollama" || got.Model != "llama3" {
		t.Fatalf("load = (%+v, %v), want the legacy choice", got, err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the legacy file is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-provider.json")); err != nil {
		t.Errorf("the choice was not moved to agent-provider.json: %v", err)
	}
}

func TestAuthPickerSelections(t *testing.T) {
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	// Cursor 0 = API key: Enter → entering; type, Enter → authAPIKey +
	// value.
	m := newAuthModel()
	m = m.updated(enter)
	if !m.entering {
		t.Fatal("selecting API key should enter text-input mode")
	}
	m.input.SetValue("sk-ant-api-typed")
	m = m.updated(enter)
	if m.result.choice != authAPIKey || m.result.apiKey != "sk-ant-api-typed" {
		t.Fatalf("api key result = %+v", m.result)
	}
	// Down to Ollama → authOllama.
	m = newAuthModel()
	m = m.updated(tea.KeyMsg{Type: tea.KeyDown}).updated(enter)
	if m.result.choice != authOllama {
		t.Fatalf("second option = %v, want authOllama", m.result.choice)
	}
	for _, o := range newAuthModel().options {
		if strings.Contains(o.label, "Claude") {
			t.Errorf("the picker offers %q, which is no longer a provider", o.label)
		}
	}

	// q cancels.
	if got := newAuthModel().updated(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}).result; got.choice != authCancel {
		t.Fatalf("q = %v, want authCancel", got.choice)
	}
}

// A Claude Code token in the environment is not a model credential any
// more: with nothing else, the agent asks for a provider.
func TestClaudeCodeTokenIsNotACredential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-ambient-shared")
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))
	if _, err := buildLocalModel(context.Background(), ""); !errors.Is(err, errNeedAuth) {
		t.Fatalf("err = %v, want errNeedAuth", err)
	}
}

// updated is a test helper: apply one message and return the concrete model.
func (m authModel) updated(msg tea.Msg) authModel {
	next, _ := m.Update(msg)
	return next.(authModel)
}
