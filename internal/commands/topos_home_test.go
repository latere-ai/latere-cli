// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestEnsureDefaultAgent(t *testing.T) {
	var creates int32
	exists := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{"agents": []map[string]any{}}
		if exists {
			out = map[string]any{"agents": []map[string]any{{"id": "agent_def", "display_name": "Assistant"}}}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&creates, 1)
		exists = true
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "agent_def", "display_name": "Assistant"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := api.NewClient(srv.URL)
	c.Token = "t"
	ctx := context.Background()

	// First call: no agents -> creates the default.
	id, err := ensureDefaultAgent(ctx, c)
	if err != nil || id != "agent_def" {
		t.Fatalf("first ensureDefaultAgent = (%q, %v), want agent_def", id, err)
	}
	// Second call: reuses the existing one, no new create.
	id2, err := ensureDefaultAgent(ctx, c)
	if err != nil || id2 != "agent_def" {
		t.Fatalf("second ensureDefaultAgent = (%q, %v), want agent_def", id2, err)
	}
	if n := atomic.LoadInt32(&creates); n != 1 {
		t.Fatalf("created the default agent %d times, want exactly 1", n)
	}
}

func TestBuildHomeRowsGroupsAndOrders(t *testing.T) {
	sessions := []interactiveSessionDTO{
		{ID: "sess_done", AgentID: "a1", Status: "completed", CreatedAt: "2026-06-01"},
		{ID: "sess_wait", AgentID: "a1", Status: "awaiting_input", CreatedAt: "2026-06-02"},
		{ID: "sess_run", AgentID: "a2", Status: "running", CreatedAt: "2026-06-03"},
	}
	agents := []agentDTO{
		{ID: "a1", DisplayName: "Build Bot", Kind: "worker"},
		{ID: "a2", DisplayName: "Helper", Kind: "assistant"},
	}
	rows := buildHomeRows(sessions, agents)

	// Order: needs-input first, then running, then recent, then agents.
	if rows[0].id != "sess_wait" {
		t.Fatalf("row[0] = %q, want the awaiting_input session first", rows[0].id)
	}
	if rows[1].id != "sess_run" {
		t.Fatalf("row[1] = %q, want the running session", rows[1].id)
	}
	if rows[2].id != "sess_done" {
		t.Fatalf("row[2] = %q, want the recent session", rows[2].id)
	}
	// Agents come last and are marked.
	last := rows[len(rows)-1]
	if !last.isAgent || last.id != "a2" {
		t.Fatalf("last row = %+v, want agent a2", last)
	}
	// Session rows resolve the agent's display name.
	if rows[0].title != "Build Bot" {
		t.Fatalf("session title = %q, want resolved agent name", rows[0].title)
	}
	// A synthetic "New session" default row (isAgent, empty id) is always present.
	sawDefault := false
	for _, r := range rows {
		if r.isAgent && r.id == "" {
			sawDefault = true
		}
	}
	if !sawDefault {
		t.Fatal("expected a default 'New session' row (empty agent id)")
	}
}

func TestHomeModelSelectSessionAttaches(t *testing.T) {
	m := newHomeModel(
		[]interactiveSessionDTO{{ID: "sess_x", AgentID: "a1", Status: "awaiting_input"}},
		[]agentDTO{{ID: "a1", DisplayName: "Bot"}},
	)
	// Cursor starts on the first row (the session); Enter attaches it.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should quit the picker")
	}
	res := updated.(homeModel).result
	if res.action != homeAttach || res.sessionID != "sess_x" {
		t.Fatalf("result = %+v, want attach sess_x", res)
	}
}

func TestHomeModelDefaultNewSession(t *testing.T) {
	// Zero agents: the only row is the synthetic "New session"; Enter starts on
	// the default agent (empty agentID, resolved later by ensureDefaultAgent).
	m := newHomeModel(nil, nil)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should quit the picker")
	}
	res := updated.(homeModel).result
	if res.action != homeStart || res.agentID != "" {
		t.Fatalf("result = %+v, want start with empty agentID (default assistant)", res)
	}
}

func TestHomeModelSelectNamedAgent(t *testing.T) {
	// rows: [New session(""), a1]. Move down to the named agent, then start it.
	m := newHomeModel(nil, []agentDTO{{ID: "a1", DisplayName: "Bot"}})
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mm.(homeModel)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	res := updated.(homeModel).result
	if res.action != homeStart || res.agentID != "a1" {
		t.Fatalf("result = %+v, want start a1", res)
	}
}

func TestHomeModelNavigationAndQuit(t *testing.T) {
	m := newHomeModel(
		[]interactiveSessionDTO{{ID: "sess_x", AgentID: "a1", Status: "running"}},
		[]agentDTO{{ID: "a1", DisplayName: "Bot"}},
	)
	// Down moves to the agent row; Enter starts it.
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mm.(homeModel)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after down", m.cursor)
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := mm.(homeModel).result; got.action != homeStart {
		t.Fatalf("action = %v, want start on the agent row", got.action)
	}

	// 'q' quits, 'r' refreshes.
	if got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); got.(homeModel).result.action != homeQuit {
		t.Fatal("q should quit")
	}
	if got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}); got.(homeModel).result.action != homeRefresh {
		t.Fatal("r should refresh")
	}
}

func TestHomeViewRendersGroupsAndKeys(t *testing.T) {
	m := newHomeModel(
		[]interactiveSessionDTO{{ID: "sess_x", AgentID: "a1", Status: "awaiting_input"}},
		[]agentDTO{{ID: "a1", DisplayName: "Build Bot"}},
	)
	out := m.View()
	for _, want := range []string{"Topos", "Needs your input", "Start a new session", "Build Bot", "[enter]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("view missing %q:\n%s", want, out)
		}
	}
}

func TestHomeViewFreshUser(t *testing.T) {
	// A brand-new user (no sessions, no agents) is never a dead-end: the home
	// always offers the default "New session".
	m := newHomeModel(nil, nil)
	out := m.View()
	for _, want := range []string{"Start a new session", "New session"} {
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
