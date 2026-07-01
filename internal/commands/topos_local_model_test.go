// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"latere.ai/x/topos/models"
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
	noop := func(models.Model) error { return nil }
	cur := "claude-opus-4-8"

	if quit := handleLocalCommand(context.Background(), "/quit", &cur, noop); !quit {
		t.Fatal("/quit should end the session")
	}
	if quit := handleLocalCommand(context.Background(), "/exit", &cur, noop); !quit {
		t.Fatal("/exit should end the session")
	}
	// Non-terminating commands return false.
	if quit := handleLocalCommand(context.Background(), "/help", &cur, noop); quit {
		t.Fatal("/help should not end the session")
	}
	if quit := handleLocalCommand(context.Background(), "/bogus", &cur, noop); quit {
		t.Fatal("unknown command should not end the session")
	}
}
