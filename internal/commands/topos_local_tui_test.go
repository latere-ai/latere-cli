// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"latere.ai/x/topos"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models/fake"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestLocalTUIFrameLayout renders a full frame and asserts the structural
// properties that can't be eyeballed in CI: no line overflows the terminal
// width (the viewport horizontal-scrolls otherwise) and Markdown is actually
// rendered (bold markers stripped), not shown raw.
func TestLocalTUIFrameLayout(t *testing.T) {
	m := newTestTUI(t)
	m.turnVerb = "Churning"
	m.Update(localEventMsg{ev: event(t, topos.EventTextDelta, textDeltaPayload{
		Text: "A **bold** word and a paragraph long enough that it must wrap at the viewport width rather than running off the right edge of the terminal, plus a `code` span.",
	})})
	m.Update(localEventMsg{ev: event(t, "PreToolUse", preToolUsePayload{ToolCall: toolCall{Name: "bash", Input: json.RawMessage(`{"command":"go build ./..."}`)}})})
	m.Update(localTurnDoneMsg{elapsed: 3 * time.Second})

	frame := ansiRE.ReplaceAllString(m.View(), "")
	for i, ln := range strings.Split(frame, "\n") {
		if w := len([]rune(strings.TrimRight(ln, " "))); w > m.width {
			t.Fatalf("line %d width %d exceeds terminal width %d: %q", i, w, m.width, ln)
		}
	}
	if strings.Contains(frame, "**bold**") {
		t.Fatal("Markdown not rendered: literal **bold** present in frame")
	}
	if !strings.Contains(frame, "bash") || !strings.Contains(frame, "3s") {
		t.Fatalf("frame missing expected content (tool + timing):\n%s", frame)
	}
}

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

// rawText concatenates the raw text of every transcript block, for assertions.
func (m *localTUI) rawText() string {
	var b strings.Builder
	for _, blk := range m.blocks {
		b.WriteString(blk.text)
		b.WriteByte('\n')
	}
	return b.String()
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

	// Assistant text streams into a single open assistant block.
	m.Update(localEventMsg{ev: event(t, topos.EventTextDelta, textDeltaPayload{Text: "hello "})})
	m.Update(localEventMsg{ev: event(t, topos.EventTextDelta, textDeltaPayload{Text: "world"})})
	if a := m.openAssistant(); a == nil || a.text != "hello world" {
		t.Fatalf("assistant block = %+v, want streamed 'hello world'", a)
	}

	// A tool call closes the assistant block and records name + arg hint.
	m.Update(localEventMsg{ev: event(t, "PreToolUse", preToolUsePayload{
		ToolCall: toolCall{Name: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
	})})
	if m.openAssistant() != nil {
		t.Fatal("a tool call should close the assistant block")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blkTool || last.name != "glob" || !strings.Contains(last.args, "*.go") {
		t.Fatalf("tool block = %+v, want glob(*.go)", last)
	}

	// A tool result is captured as a result block.
	m.Update(localEventMsg{ev: event(t, topos.EventPostToolUse, postToolUsePayload{
		ToolCall: toolCall{Name: "glob"},
		Result:   toolResult{Content: "a.go b.go"},
	})})
	if last := m.blocks[len(m.blocks)-1]; last.kind != blkResult || last.text != "a.go b.go" {
		t.Fatalf("result block = %+v, want the tool content", last)
	}

	// Usage updates the status counters without adding a block.
	n := len(m.blocks)
	m.Update(localEventMsg{ev: event(t, topos.EventUsage, usagePayload{Total: usageTotals{InputTokens: 12, OutputTokens: 7}})})
	if m.inTok != 12 || m.outTok != 7 {
		t.Fatalf("usage = %d/%d, want 12/7", m.inTok, m.outTok)
	}
	if len(m.blocks) != n {
		t.Fatal("usage should not add a transcript block")
	}
}

func TestLocalTUINoticeClosesAssistant(t *testing.T) {
	m := newTestTUI(t)
	m.Update(localEventMsg{ev: event(t, topos.EventTextDelta, textDeltaPayload{Text: "text"})})
	m.onSlash("/help")
	// The assistant block is separate from the help notice (blocks can't concatenate).
	if m.openAssistant() != nil {
		t.Fatal("appending a notice should settle the assistant block")
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blkNotice || !strings.Contains(last.text, "Commands:") {
		t.Fatalf("last block = %+v, want the help notice", last)
	}
}

func TestBannerRows(t *testing.T) {
	m := newTestTUI(t)
	// The banner is three lines (wordmark, model, cwd) and scrolls in the
	// transcript rather than being a fixed header.
	if got := strings.Count(m.bannerView(), "\n") + 1; got != 3 {
		t.Fatalf("banner rows = %d, want 3", got)
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
	for range 100 { // fill well past one screen
		m.appendNotice("line", false)
	}
	m.refresh()
	if !m.vp.AtBottom() {
		t.Fatal("new content should pin to the bottom")
	}
	// ↑ must scroll the transcript (routed to the viewport, not the input).
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.vp.AtBottom() {
		t.Fatal("↑ should scroll the transcript up, away from the bottom")
	}
}

func TestLocalTUIResultFolding(t *testing.T) {
	m := newTestTUI(t)
	long := strings.Repeat("x\n", collapseThreshold+5)
	m.Update(localEventMsg{ev: event(t, topos.EventPostToolUse, postToolUsePayload{
		ToolCall: toolCall{Name: "bash"},
		Result:   toolResult{Content: long},
	})})
	// Collapsed by default: summary mentions the hidden line count.
	if got := m.renderBlock(len(m.blocks) - 1); !strings.Contains(got, "lines") {
		t.Fatalf("collapsed result = %q, want a '+N lines' summary", got)
	}
	// Ctrl+O expands: the full content is shown (more rows than the summary).
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	expanded := m.renderBlock(len(m.blocks) - 1)
	if strings.Count(expanded, "\n") < collapseThreshold {
		t.Fatalf("expanded result should show all lines, got %q", expanded)
	}
}

func TestLocalTUITurnDone(t *testing.T) {
	m := newTestTUI(t)
	m.running = true
	m.turnVerb = "Churning"

	// A successful turn stops running and appends the timing footer.
	m.outTok = 42
	m.Update(localTurnDoneMsg{transcript: nil, elapsed: 3 * time.Second})
	if m.running {
		t.Fatal("turn done should clear running")
	}
	if got := m.rawText(); !strings.Contains(got, "3s") || !strings.Contains(got, "42 tok") {
		t.Fatalf("footer = %q, want elapsed + tokens", got)
	}

	// A failed turn surfaces the error.
	m.running = true
	m.Update(localTurnDoneMsg{err: context.Canceled})
	if !strings.Contains(m.rawText(), "error:") {
		t.Fatalf("transcript = %q, want error notice", m.rawText())
	}
}

func TestLocalTUISlashCommands(t *testing.T) {
	m := newTestTUI(t)

	// /model <name> switches the active model in place.
	m.onSlash("/model claude-switched-9")
	if m.curModel != "claude-switched-9" {
		t.Fatalf("curModel = %q, want claude-switched-9", m.curModel)
	}
	if !strings.Contains(m.rawText(), "switched to claude-switched-9") {
		t.Fatalf("transcript missing switch notice: %q", m.rawText())
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
