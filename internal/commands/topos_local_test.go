// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models"
	"latere.ai/x/topos/models/scripted"
)

func TestBuildLocalModelCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Isolate the on-disk config so a real login can't leak in.
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))
	if _, err := buildLocalModel(context.Background(), ""); err == nil {
		t.Fatal("expected an error with no credentials")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	m, err := buildLocalModel(context.Background(), "")
	if err != nil || m.name != defaultAnthropicModel {
		t.Fatalf("buildLocalModel with API key = (%q, %v), want %s", m.name, err, defaultAnthropicModel)
	}
	_, conn, entry, err := m.connect(t.Context())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if conn.BaseURL != anthropicURL || conn.Credential != "sk-test" || conn.Model != defaultAnthropicModel || entry.InputWindow == 0 {
		t.Fatalf("the direct connection is %+v with figures %+v", conn, entry)
	}
	// A model the catalog does not know is refused at the turn, not
	// sent with no figures.
	unknown, err := buildLocalModel(t.Context(), "claude-unknown-0")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := unknown.connect(t.Context()); err == nil {
		t.Fatal("an unknown model connected")
	}
}

// scriptedModel is a local model that plays script, one step per request,
// in place of a provider.
func scriptedModel(t *testing.T, script string) localModel {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.yaml")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	player := &scripted.Model{}
	return localModel{name: "scripted", connect: func(context.Context) (models.Model, models.Connection, models.Entry, error) {
		return player, models.Connection{BaseURL: "scripted:" + path, Model: "scripted", Family: models.FamilyOther},
			models.Entry{Name: "scripted", Family: models.FamilyOther, InputWindow: 200_000, MaxOutputTokens: 32_000}, nil
	}}
}

// recorder keeps the events a local agent emits.
type recorder struct {
	mu     sync.Mutex
	events []localEvent
}

func (r *recorder) emit(e localEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) all() []localEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]localEvent(nil), r.events...)
}

// TestLocalLoopEndToEnd drives the whole local stack with a scripted model:
// the model runs a command and writes a file, the host machine really runs
// both in the working directory, neither asks for a confirmation, and the
// terminal is told the streamed text, each call and its result, and the
// tokens. Only the model is played from a script.
func TestLocalLoopEndToEnd(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{}
	model := scriptedModel(t, `steps:
  - tool_calls:
      - name: bash
        input: {command: "echo marker-12345"}
      - name: write
        input: {path: note.txt, content: "hi"}
    usage: {input_tokens: 10, output_tokens: 4}
  - text: "Done."
    usage: {input_tokens: 20, output_tokens: 2}
`)
	agent, err := newLocalAgent(t.Context(), dir, model, rec.emit)
	if err != nil {
		t.Fatalf("newLocalAgent: %v", err)
	}
	data := agent.dataDir
	if err := agent.turn(t.Context(), "run it"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := agent.close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "note.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("note.txt = %q, %v; want the file the model wrote", got, err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("the agent's data directory outlived it: %v", err)
	}
	var calls, text, results strings.Builder
	var usage localEvent
	for _, e := range rec.all() {
		switch e.kind {
		case localText:
			text.WriteString(e.text)
		case localToolUse:
			calls.WriteString(e.name + " ")
		case localToolResult:
			if e.isErr {
				t.Errorf("a call failed: %s", e.text)
			}
			results.WriteString(e.text + "\n")
		case localUsage:
			usage = e
		}
	}
	if calls.String() != "bash write " {
		t.Errorf("calls = %q, want bash then write", calls.String())
	}
	if !strings.Contains(results.String(), "marker-12345") {
		t.Errorf("results %q lack the command's output (the host ran it)", results.String())
	}
	if text.String() != "Done." {
		t.Errorf("streamed text = %q, want Done.", text.String())
	}
	if usage.in != 30 || usage.out != 6 {
		t.Errorf("usage = %d in, %d out; want the session's 30 and 6", usage.in, usage.out)
	}
}

// A turn that ends any other way than its end is the turn's error, so the
// terminal shows it.
func TestLocalTurnReportsAStoppedTurn(t *testing.T) {
	agent, err := newLocalAgent(t.Context(), t.TempDir(), scriptedModel(t, "steps:\n  - fail: {status: 400}\n"), func(localEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := agent.turn(t.Context(), "hello"); err == nil {
		t.Fatal("a turn whose model failed reported success")
	}
}

// TestLocalSystemPromptNamesNoTool keeps tool names out of the system
// prompt: each tool's description already says what it does, and a name in
// prose becomes a dangling reference the moment the tool set changes.
func TestLocalSystemPromptNamesNoTool(t *testing.T) {
	for _, tool := range tools.Builtins() {
		if name := tool.Definition().Name; strings.Contains(localSystemPrompt, name) {
			t.Errorf("localSystemPrompt names tool %q; describe the behavior instead", name)
		}
	}
}
