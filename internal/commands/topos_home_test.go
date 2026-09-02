// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBuildHomeRowsNewSessionFirstThenGrouped(t *testing.T) {
	sessions := []interactiveSessionDTO{
		{ID: "sess_done", Status: "completed", CreatedAt: "2026-06-01"},
		{ID: "sess_wait", Status: "awaiting_input", CreatedAt: "2026-06-02"},
		{ID: "sess_run", Status: "running", CreatedAt: "2026-06-03"},
	}
	rows := buildHomeRows(sessions)

	// "New session" is always first.
	if !rows[0].newSession {
		t.Fatalf("row[0] = %+v, want the New session action first", rows[0])
	}
	// Then sessions by urgency: needs-input, running, recent.
	if rows[1].sessionID != "sess_wait" || rows[2].sessionID != "sess_run" || rows[3].sessionID != "sess_done" {
		t.Fatalf("session order wrong: %q %q %q", rows[1].sessionID, rows[2].sessionID, rows[3].sessionID)
	}
}

func TestHomeNewSessionStarts(t *testing.T) {
	// Fresh user (no sessions): the only row is "New session"; Enter starts.
	m := newHomeModel(nil)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should quit the picker")
	}
	if got := updated.(homeModel).result; got.action != homeStart {
		t.Fatalf("result = %+v, want homeStart", got)
	}
}

func TestHomeResumeSession(t *testing.T) {
	// rows: [New session, sess_x]. Down to the session, Enter resumes it.
	m := newHomeModel([]interactiveSessionDTO{{ID: "sess_x", Status: "awaiting_input"}})
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mm.(homeModel)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(homeModel).result
	if got.action != homeAttach || got.sessionID != "sess_x" {
		t.Fatalf("result = %+v, want attach sess_x", got)
	}
}

func TestHomeQuitAndRefresh(t *testing.T) {
	m := newHomeModel(nil)
	if got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); got.(homeModel).result.action != homeQuit {
		t.Fatal("q should quit")
	}
	if got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}); got.(homeModel).result.action != homeRefresh {
		t.Fatal("r should refresh")
	}
}

func TestHomeViewFreshUser(t *testing.T) {
	// A brand-new user (no sessions) is never a dead-end: "New session" is there.
	out := newHomeModel(nil).View()
	for _, want := range []string{"Topos", "Start", "New session", "[enter]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("fresh-user view missing %q:\n%s", want, out)
		}
	}
}

func TestFriendlyStatus(t *testing.T) {
	cases := map[string]string{"awaiting_input": "waiting for you", "running": "working", "completed": "done", "weird": "weird"}
	for in, want := range cases {
		if got := friendlyStatus(in); got != want {
			t.Errorf("friendlyStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
