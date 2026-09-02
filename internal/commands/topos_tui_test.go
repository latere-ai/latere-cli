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

// recSender records control messages the model sends.
type recSender struct{ sent []attachControl }

func (r *recSender) Send(c attachControl) error { r.sent = append(r.sent, c); return nil }

func newTestModel(readonly bool) (tuiModel, *recSender) {
	snd := &recSender{}
	ch := make(chan streamMsg)
	return newTUIModel("sess_1", ch, snd, readonly), snd
}

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestTUIEnterSendsUserTurn(t *testing.T) {
	m, snd := newTestModel(false)
	m.input.SetValue("hello agent")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	if len(snd.sent) != 1 || snd.sent[0].Type != "user_turn" || snd.sent[0].Text != "hello agent" {
		t.Fatalf("sent = %+v, want one user_turn", snd.sent)
	}
	if m.input.Value() != "" {
		t.Fatal("input should reset after send")
	}
	// The message is echoed into the transcript.
	if len(m.state.lines) == 0 || !strings.Contains(m.state.lines[0], "hello agent") {
		t.Fatalf("transcript = %v, want the echoed user message", m.state.lines)
	}
}

func TestTUIEnterEmptyDoesNothing(t *testing.T) {
	m, snd := newTestModel(false)
	m.input.SetValue("   ")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if len(snd.sent) != 0 {
		t.Fatalf("blank input must not send, got %+v", snd.sent)
	}
}

func TestTUIEscInterrupts(t *testing.T) {
	m, snd := newTestModel(false)
	if _, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc}); len(snd.sent) != 1 || snd.sent[0].Type != "interrupt" {
		t.Fatalf("sent = %+v, want interrupt", snd.sent)
	}
}

func TestTUIApprovalKeys(t *testing.T) {
	m, snd := newTestModel(false)
	// Deliver an approval request frame.
	updated, _ := m.Update(streamEventMsg{ok: true, m: streamMsg{frame: &attachFrame{
		Type: "event", Event: "ApprovalRequest", Payload: []byte(`{"decision_id":"d9","tool_id":"bash"}`),
	}}})
	m = updated.(tuiModel)
	if m.state.pending == nil {
		t.Fatal("model should have a pending approval")
	}
	// 'y' approves.
	updated, _ = m.Update(keyRunes("y"))
	m = updated.(tuiModel)
	if len(snd.sent) != 1 || snd.sent[0].Type != "approval_reply" || !snd.sent[0].Approve || snd.sent[0].DecisionID != "d9" {
		t.Fatalf("sent = %+v, want approve d9", snd.sent)
	}
	if m.state.pending != nil {
		t.Fatal("pending should clear after reply")
	}
}

func TestTUIApprovalDenyKey(t *testing.T) {
	m, snd := newTestModel(false)
	m.state.pending = &approvalRequestPayload{DecisionID: "d1", ToolID: "bash"}
	m.state.status = "awaiting approval"
	updated, _ := m.Update(keyRunes("n"))
	_ = updated
	if len(snd.sent) != 1 || snd.sent[0].Approve {
		t.Fatalf("sent = %+v, want a deny", snd.sent)
	}
}

func TestTUIReadonlyIgnoresInput(t *testing.T) {
	m, snd := newTestModel(true)
	m.input.SetValue("nope")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(snd.sent) != 0 {
		t.Fatalf("read-only must not send, got %+v", snd.sent)
	}
}

func TestTUIFrameUpdatesTranscriptAndCloseQuits(t *testing.T) {
	m, _ := newTestModel(false)
	updated, _ := m.Update(streamEventMsg{ok: true, m: streamMsg{frame: &attachFrame{
		Type: "event", Event: "AssistantMessage", Payload: []byte(`{"text":"world"}`),
	}}})
	m = updated.(tuiModel)
	if len(m.state.lines) == 0 || !strings.Contains(m.state.lines[0], "world") {
		t.Fatalf("transcript = %v", m.state.lines)
	}

	// A closed status quits.
	updated, cmd := m.Update(streamEventMsg{ok: true, m: streamMsg{frame: &attachFrame{Type: "status", State: "closed"}}})
	m = updated.(tuiModel)
	if !m.quitting || cmd == nil {
		t.Fatal("closed status should quit")
	}
}

func TestTUIStreamClosedQuits(t *testing.T) {
	m, _ := newTestModel(false)
	updated, cmd := m.Update(streamEventMsg{ok: false})
	m = updated.(tuiModel)
	if !m.quitting || cmd == nil {
		t.Fatal("stream-closed should quit")
	}
}

func TestTUINoteUpdatesStatus(t *testing.T) {
	m, _ := newTestModel(false)
	updated, _ := m.Update(streamEventMsg{ok: true, m: streamMsg{note: "reconnecting"}})
	m = updated.(tuiModel)
	if m.state.status != "reconnecting" {
		t.Fatalf("status = %q, want reconnecting", m.state.status)
	}
}

func TestTUIViewRenders(t *testing.T) {
	m, _ := newTestModel(false)
	m.width, m.height = 80, 24
	m.state.lines = []string{"● hi"}
	out := m.View()
	if !strings.Contains(out, "hi") || !strings.Contains(out, "sess_1") {
		t.Fatalf("view = %q", out)
	}
	// Quitting view shows the reattach hint.
	m.quitting = true
	if !strings.Contains(m.View(), "attach sess_1") {
		t.Fatalf("quitting view = %q, want reattach hint", m.View())
	}
}

func TestTUIWindowSize(t *testing.T) {
	m, _ := newTestModel(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(tuiModel)
	if m.width != 120 || m.height != 40 {
		t.Fatalf("size = %dx%d, want 120x40", m.width, m.height)
	}
}

func TestTUIInitAndWaitEvent(t *testing.T) {
	m, _ := newTestModel(false)
	if m.Init() == nil {
		t.Fatal("Init should return a command")
	}
	// waitEvent returns the next message; ok=true with a frame.
	ch := make(chan streamMsg, 1)
	ch <- streamMsg{note: "connected"}
	msg := waitEvent(ch)()
	se, ok := msg.(streamEventMsg)
	if !ok || !se.ok || se.m.note != "connected" {
		t.Fatalf("waitEvent msg = %+v", msg)
	}
	// A closed channel yields ok=false.
	close(ch)
	if se := waitEvent(ch)().(streamEventMsg); se.ok {
		t.Fatal("waitEvent on closed channel should report ok=false")
	}
}

func TestTUITypingUpdatesInput(t *testing.T) {
	m, _ := newTestModel(false)
	updated, _ := m.Update(keyRunes("h"))
	m = updated.(tuiModel)
	updated, _ = m.Update(keyRunes("i"))
	m = updated.(tuiModel)
	if m.input.Value() != "hi" {
		t.Fatalf("input = %q, want hi", m.input.Value())
	}
}

func TestTUIViewApprovalAndReadonly(t *testing.T) {
	m, _ := newTestModel(false)
	m.width, m.height = 80, 24
	m.state.pending = &approvalRequestPayload{DecisionID: "d", ToolID: "bash"}
	if !strings.Contains(m.View(), "approve tool bash") {
		t.Fatalf("view should show the approval prompt: %q", m.View())
	}
	ro, _ := newTestModel(true)
	ro.width, ro.height = 80, 24
	if !strings.Contains(ro.View(), "read-only") {
		t.Fatalf("read-only view = %q", ro.View())
	}
}

func TestTUICtrlCQuits(t *testing.T) {
	m, _ := newTestModel(false)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(tuiModel)
	if !m.quitting || cmd == nil {
		t.Fatal("Ctrl+C should quit")
	}
}
