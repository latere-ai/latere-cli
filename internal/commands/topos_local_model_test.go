// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelPickerSelection(t *testing.T) {
	list := []string{"claude-opus-4-8", "claude-haiku-4-5", "claude-fable-5"}

	// Starts on the current model.
	mp := newModelPicker(list, "claude-haiku-4-5")
	if mp.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (the current model)", mp.cursor)
	}

	// Down then Enter selects the third model.
	m2, _ := mp.Update(tea.KeyMsg{Type: tea.KeyDown})
	m3, cmd := m2.(modelPicker).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m3.(modelPicker).chosen; got != "claude-fable-5" {
		t.Fatalf("chosen = %q, want claude-fable-5", got)
	}
	if cmd == nil {
		t.Fatal("enter should quit the program")
	}

	// q cancels with no choice.
	mc, _ := newModelPicker(list, "").Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if mc.(modelPicker).chosen != "" {
		t.Fatal("q should not choose a model")
	}
}

func TestHandleLocalCommand(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	agent, err := newLocalAgent(t.Context(), t.TempDir(), anthropicDirect("sk-test", "claude-opus-4-8"), func(localEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if quit := handleLocalCommand(context.Background(), "/quit", agent); !quit {
		t.Fatal("/quit should end the session")
	}
	if quit := handleLocalCommand(context.Background(), "/exit", agent); !quit {
		t.Fatal("/exit should end the session")
	}
	// Non-terminating commands return false.
	if quit := handleLocalCommand(context.Background(), "/help", agent); quit {
		t.Fatal("/help should not end the session")
	}
	if quit := handleLocalCommand(context.Background(), "/bogus", agent); quit {
		t.Fatal("unknown command should not end the session")
	}
	// /model <name> switches the model the next turn runs on.
	if quit := handleLocalCommand(context.Background(), "/model claude-haiku-4-5", agent); quit || agent.modelName() != "claude-haiku-4-5" {
		t.Fatalf("/model switched to %q", agent.modelName())
	}
}
