// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"latere.ai/x/topos"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models/fake"
)

// newTestTUI builds a sized localTUI with a fake brain for driving Update.
func newTestTUI(t *testing.T) *localTUI {
	t.Helper()
	// A deterministic credential so /model switches resolve without network.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("LATERE_TOPOS_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	sb, err := newHostSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := newLocalTUI(context.Background(), "9.9.9", "/work", sb, tools.Builtins(), fake.New())
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30}) // size the layout
	return m
}

func event(t *testing.T, name string, payload any) topos.Event {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return topos.Event{Name: name, PayloadJSON: b}
}

func TestLocalTUISizingAndStreaming(t *testing.T) {
	m := newTestTUI(t)
	if !m.ready {
		t.Fatal("WindowSizeMsg should mark the layout ready")
	}
	if m.vp.Height <= 0 || m.vp.Width != 100 {
		t.Fatalf("viewport not sized: %dx%d", m.vp.Width, m.vp.Height)
	}

	// Assistant text streams into the buffer and opens a text block.
	m.Update(localEventMsg{ev: event(t, topos.EventTextDelta, textDeltaPayload{Text: "hello "})})
	m.Update(localEventMsg{ev: event(t, topos.EventTextDelta, textDeltaPayload{Text: "world"})})
	if got := m.buf.String(); !strings.Contains(got, "hello world") {
		t.Fatalf("buffer = %q, want streamed text", got)
	}
	if !m.inText {
		t.Fatal("a text block should be open after deltas")
	}

	// A tool call closes the text block and shows name + arg hint.
	m.Update(localEventMsg{ev: event(t, "PreToolUse", preToolUsePayload{
		ToolCall: toolCall{Name: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
	})})
	if m.inText {
		t.Fatal("a tool call should close the text block")
	}
	if got := m.buf.String(); !strings.Contains(got, "glob") || !strings.Contains(got, "*.go") {
		t.Fatalf("buffer = %q, want tool name + arg", got)
	}

	// A tool result hangs under the branch.
	m.Update(localEventMsg{ev: event(t, topos.EventPostToolUse, postToolUsePayload{
		ToolCall: toolCall{Name: "glob"},
		Result:   toolResult{Content: "a.go b.go"},
	})})
	if got := m.buf.String(); !strings.Contains(got, "a.go b.go") {
		t.Fatalf("buffer = %q, want tool result", got)
	}

	// Usage updates the status counters without touching the transcript.
	before := m.buf.String()
	m.Update(localEventMsg{ev: event(t, topos.EventUsage, usagePayload{Total: usageTotals{InputTokens: 12, OutputTokens: 7}})})
	if m.inTok != 12 || m.outTok != 7 {
		t.Fatalf("usage = %d/%d, want 12/7", m.inTok, m.outTok)
	}
	if m.buf.String() != before {
		t.Fatal("usage should not append to the transcript")
	}
}

func TestLocalTUIClosesTextBeforeNotice(t *testing.T) {
	m := newTestTUI(t)
	// Streamed assistant text ends without a newline...
	m.Update(localEventMsg{ev: event(t, topos.EventTextDelta, textDeltaPayload{Text: "help you with today?"})})
	// ...then a slash-command notice must start on its own line, not concatenate.
	m.onSlash("/help")
	if strings.Contains(m.buf.String(), "today?Commands") {
		t.Fatalf("notice ran onto the assistant line: %q", m.buf.String())
	}
	if !strings.Contains(m.buf.String(), "today?\n") {
		t.Fatalf("assistant block not closed with a newline: %q", m.buf.String())
	}
}

func TestHeaderRowsMatchLayoutBudget(t *testing.T) {
	m := newTestTUI(t)
	// headerView must render exactly the 3 rows layout() budgets, else the input
	// box or status line is pushed off / cut.
	if got := strings.Count(m.headerView(), "\n") + 1; got != 3 {
		t.Fatalf("header rows = %d, want 3", got)
	}
}

func TestHomeAbbrev(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	cases := map[string]string{
		"/Users/x":            "~",
		"/Users/x/dev/agents": "~/dev/agents",
		"/other/path":         "/other/path",
		"/Users/xyz":          "/Users/xyz", // not a subpath of the home dir
	}
	for in, want := range cases {
		if got := homeAbbrev(in); got != want {
			t.Fatalf("homeAbbrev(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalTUIScrolls(t *testing.T) {
	m := newTestTUI(t)
	for i := 0; i < 100; i++ { // fill well past one screen
		m.appendLine("line")
	}
	if !m.vp.AtBottom() {
		t.Fatal("new content should pin to the bottom")
	}
	// ↑ must scroll the transcript (routed to the viewport, not the input).
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.vp.AtBottom() {
		t.Fatal("↑ should scroll the transcript up, away from the bottom")
	}
}

func TestLocalTUITurnDone(t *testing.T) {
	m := newTestTUI(t)
	m.running = true

	// A failed turn stops running and surfaces the error in the transcript.
	m.Update(localTurnDoneMsg{err: context.Canceled})
	if m.running {
		t.Fatal("turn done should clear running")
	}
	if !strings.Contains(m.buf.String(), "error:") {
		t.Fatalf("buffer = %q, want error line", m.buf.String())
	}
}

func TestLocalTUISlashCommands(t *testing.T) {
	m := newTestTUI(t)

	// /help lists commands.
	m.onSlash("/help")
	if !strings.Contains(m.buf.String(), "Commands:") {
		t.Fatalf("/help buffer = %q", m.buf.String())
	}

	// /model <name> switches the active model in place.
	m.onSlash("/model claude-switched-9")
	if m.curModel != "claude-switched-9" {
		t.Fatalf("curModel = %q, want claude-switched-9", m.curModel)
	}
	if !strings.Contains(m.buf.String(), "switched to claude-switched-9") {
		t.Fatalf("buffer missing switch notice: %q", m.buf.String())
	}

	// /quit asks the program to quit.
	_, cmd := m.onSlash("/quit")
	if cmd == nil {
		t.Fatal("/quit should return a quit command")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("/quit cmd = %#v, want tea.Quit", msg)
	}
}
