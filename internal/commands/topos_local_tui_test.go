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
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"latere.ai/x/topos/session"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestLocalTUIFrameLayout renders a full frame and asserts the structural
// properties that can't be eyeballed in CI: no line overflows the terminal
// width (the viewport horizontal-scrolls otherwise) and Markdown is actually
// rendered (bold markers stripped), not shown raw.
func TestLocalTUIFrameLayout(t *testing.T) {
	m := newTestTUI(t)
	m.turnVerb = "Churning"
	m.Update(localEventMsg{ev: localEvent{kind: localText,
		text: "A **bold** word and a paragraph long enough that it must wrap at the viewport width rather than running off the right edge of the terminal, plus a `code` span.",
	}})
	m.Update(localEventMsg{ev: localEvent{kind: localToolUse, name: "bash", input: json.RawMessage(`{"command":"go build ./..."}`)}})
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

// newTestTUI builds a sized localTUI with a scripted model for driving
// Update.
func newTestTUI(t *testing.T) *localTUI {
	t.Helper()
	return newScriptedTUI(t, "steps: []\n")
}

// newScriptedTUI is newTestTUI with its model playing script.
func newScriptedTUI(t *testing.T, script string) *localTUI {
	t.Helper()
	// A deterministic credential so /model switches resolve without network.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	m, err := newLocalTUI(context.Background(), "9.9.9", t.TempDir(), scriptedModel(t, script))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
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

func TestLocalTUISizingAndStreaming(t *testing.T) {
	m := newTestTUI(t)
	if !m.ready {
		t.Fatal("WindowSizeMsg should mark the layout ready")
	}
	if m.vp.Height <= 0 || m.vp.Width != 100 {
		t.Fatalf("viewport not sized: %dx%d", m.vp.Width, m.vp.Height)
	}

	// Assistant text streams into a single open assistant block.
	m.Update(localEventMsg{ev: localEvent{kind: localText, text: "hello "}})
	m.Update(localEventMsg{ev: localEvent{kind: localText, text: "world"}})
	if a := m.openAssistant(); a == nil || a.text != "hello world" {
		t.Fatalf("assistant block = %+v, want streamed 'hello world'", a)
	}

	// A tool call closes the assistant block and records name + arg hint.
	m.Update(localEventMsg{ev: localEvent{kind: localToolUse, name: "glob", input: json.RawMessage(`{"pattern":"*.go"}`)}})
	if m.openAssistant() != nil {
		t.Fatal("a tool call should close the assistant block")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blkTool || last.name != "glob" || !strings.Contains(last.args, "*.go") {
		t.Fatalf("tool block = %+v, want glob(*.go)", last)
	}

	// A tool result is captured as a result block.
	m.Update(localEventMsg{ev: localEvent{kind: localToolResult, text: "a.go b.go"}})
	if last := m.blocks[len(m.blocks)-1]; last.kind != blkResult || last.text != "a.go b.go" {
		t.Fatalf("result block = %+v, want the tool content", last)
	}

	// Usage updates the status counters without adding a block.
	n := len(m.blocks)
	m.Update(localEventMsg{ev: localEvent{kind: localUsage, in: 12, out: 7}})
	if m.inTok != 12 || m.outTok != 7 {
		t.Fatalf("usage = %d/%d, want 12/7", m.inTok, m.outTok)
	}
	if len(m.blocks) != n {
		t.Fatal("usage should not add a transcript block")
	}
}

func TestLocalTUINoticeClosesAssistant(t *testing.T) {
	m := newTestTUI(t)
	m.Update(localEventMsg{ev: localEvent{kind: localText, text: "text"}})
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
	m.Update(localEventMsg{ev: localEvent{kind: localToolResult, text: long}})
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
	m.Update(localTurnDoneMsg{elapsed: 3 * time.Second})
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
	if got := m.agent.modelName(); got != "claude-switched-9" {
		t.Fatalf("the model is %q, want claude-switched-9", got)
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

// typeLine types line into the prompt and presses key.
func typeLine(m *localTUI, line string, key tea.KeyMsg) {
	m.input.SetValue(line)
	m.Update(key)
}

// working makes m's agent one whose turn runs: its log has a turn's
// running status, so a message sent now waits, and a drive is said to run.
func working(t *testing.T, m *localTUI) {
	t.Helper()
	st, err := session.NewEvent(session.TypeSessionStatus, session.SessionStatus{Status: session.StatusRunning}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.agent.appendChecked(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	m.agent.sendMu.Lock()
	m.agent.driving = true
	m.agent.sendMu.Unlock()
	m.running = true
}

// markRead appends the messages.read the harness writes for ids.
func markRead(t *testing.T, m *localTUI, ids ...string) {
	t.Helper()
	e, err := session.NewEvent(session.TypeMessagesRead, session.MessagesRead{EventIDs: ids}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.agent.appendChecked(t.Context(), e, nil); err != nil {
		t.Fatal(err)
	}
}

// sentNow reports the send_now of the message id in m's log.
func sentNow(t *testing.T, m *localTUI, id string) bool {
	t.Helper()
	for _, e := range logOf(t, m.agent) {
		var p session.UserMessage
		if e.ID == id && e.Decode(&p) == nil {
			return p.SendNow
		}
	}
	t.Fatalf("no message %s in the log", id)
	return false
}

// frameLines is m's frame without styles, line by line.
func frameLines(m *localTUI) []string {
	return strings.Split(ansiRE.ReplaceAllString(m.View(), ""), "\n")
}

// lineOf is the index of the first line of lines that holds s, -1 for none.
func lineOf(lines []string, s string) int {
	return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, s) })
}

// TestLocalTUIMessagesWhileItWorks sends with Enter while the agent works:
// the message waits below the conversation and above the prompt, and
// moves into the transcript where the agent read it. Ctrl+J and Alt+Enter
// send one to be read now, which is said, and so is the stop it made.
func TestLocalTUIMessagesWhileItWorks(t *testing.T) {
	m := newTestTUI(t)
	working(t, m)
	typeLine(m, "use port 9000", tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.waiting) != 1 || m.waiting[0].text != "use port 9000" || m.waiting[0].now {
		t.Fatalf("waiting = %+v, want the message", m.waiting)
	}
	if sentNow(t, m, m.waiting[0].id) {
		t.Error("Enter sent the message to be read now")
	}
	for _, b := range m.blocks {
		if b.kind == blkUser {
			t.Fatalf("the transcript shows %q before the agent read it", b.text)
		}
	}
	lines := frameLines(m)
	msg, hint, box := lineOf(lines, "use port 9000"), lineOf(lines, "reads this after its current step · ↑ to edit"), lineOf(lines, "╭")
	if msg < 0 || hint != msg+1 || box != hint+1 {
		t.Fatalf("the waiting message is at line %d, its hint at %d, the prompt at %d; want them in that order above the prompt:\n%s", msg, hint, box, strings.Join(lines, "\n"))
	}
	if len(lines) != m.height {
		t.Errorf("the frame is %d lines on a %d line terminal", len(lines), m.height)
	}
	if !strings.Contains(lines[len(lines)-1], "Ctrl+J send now") {
		t.Errorf("the status line %q does not name the send-now key while it works", lines[len(lines)-1])
	}

	id := m.waiting[0].id
	m.Update(localEventMsg{ev: localEvent{kind: localRead, id: id, text: "use port 9000"}})
	if len(m.waiting) != 0 {
		t.Fatalf("waiting = %+v after the read", m.waiting)
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blkUser || last.text != "use port 9000" || last.note != "" {
		t.Fatalf("last block = %+v, want the message where it was read", last)
	}
	if lineOf(frameLines(m), "after its current step") >= 0 {
		t.Error("the hint stayed after the agent read the message")
	}

	typeLine(m, "stop and read this", tea.KeyMsg{Type: tea.KeyCtrlJ})
	typeLine(m, "and this", tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if len(m.waiting) != 2 || !m.waiting[0].now || !m.waiting[1].now {
		t.Fatalf("waiting = %+v, want two messages sent to be read now", m.waiting)
	}
	if !sentNow(t, m, m.waiting[0].id) || !sentNow(t, m, m.waiting[1].id) {
		t.Error("Ctrl+J or Alt+Enter did not send the message to be read now")
	}
	if lineOf(frameLines(m), "stopping its current step to read these · ↑ to edit") < 0 {
		t.Errorf("no send-now hint in\n%s", strings.Join(frameLines(m), "\n"))
	}
	m.Update(localEventMsg{ev: localEvent{kind: localRead, id: m.waiting[0].id, text: "stop and read this", cut: true}})
	if lineOf(frameLines(m), "stopping its current step to read this · ↑ to edit") < 0 {
		t.Errorf("no send-now hint for the one left in\n%s", strings.Join(frameLines(m), "\n"))
	}
	if got := ansiRE.ReplaceAllString(m.renderBlock(len(m.blocks)-1), ""); !strings.Contains(got, "stopped what it was doing to read this") {
		t.Fatalf("the message its stop was for renders %q, want the note", got)
	}
}

// TestLocalTUIUpTakesBackWaitingMessages presses ↑ on an empty prompt: the
// messages the agent has not read come back into the prompt, and one it
// read first stays, with a note. With text in the prompt, ↑ takes nothing.
func TestLocalTUIUpTakesBackWaitingMessages(t *testing.T) {
	m := newTestTUI(t)
	working(t, m)
	typeLine(m, "first", tea.KeyMsg{Type: tea.KeyEnter})
	typeLine(m, "second", tea.KeyMsg{Type: tea.KeyEnter})
	typeLine(m, "third", tea.KeyMsg{Type: tea.KeyEnter})
	first, second, third := m.waiting[0].id, m.waiting[1].id, m.waiting[2].id
	markRead(t, m, second)

	m.input.SetValue("draft")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if len(m.waiting) != 3 || m.input.Value() != "draft" {
		t.Fatalf("↑ with text in the prompt took back %+v, prompt %q", m.waiting, m.input.Value())
	}

	m.input.Reset()
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "first third" {
		t.Fatalf("the prompt is %q, want the two messages the agent has not read", got)
	}
	if len(m.waiting) != 1 || m.waiting[0].id != second {
		t.Fatalf("waiting = %+v, want the read message alone", m.waiting)
	}
	if !strings.Contains(m.rawText(), "had already read this, so it stays") {
		t.Fatalf("transcript %q lacks the note on the read message", m.rawText())
	}
	evs := logOf(t, m.agent)
	if stateOf(evs, first) != session.MessageWithdrawn || stateOf(evs, third) != session.MessageWithdrawn || stateOf(evs, second) != session.MessageRead {
		t.Fatalf("states: first %s, second %s, third %s", stateOf(evs, first), stateOf(evs, second), stateOf(evs, third))
	}

	typeLine(m, "fourth", tea.KeyMsg{Type: tea.KeyEnter})
	markRead(t, m, m.waiting[1].id)
	m.input.Reset()
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !strings.Contains(m.rawText(), "had already read these, so they stay") || m.input.Value() != "" {
		t.Fatalf("transcript %q, prompt %q; want the note on both read messages and nothing back", m.rawText(), m.input.Value())
	}
}

// A send or a take-back the store refuses says so, and loses nothing the
// prompt or the waiting messages held.
func TestLocalTUIStoreFailures(t *testing.T) {
	m := newTestTUI(t)
	working(t, m)
	typeLine(m, "kept", tea.KeyMsg{Type: tea.KeyEnter})
	m.agent.store = &flakyStore{Store: m.agent.store, broken: true}
	typeLine(m, "lost", tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.waiting) != 1 || !strings.Contains(m.rawText(), "error: the store is down") {
		t.Fatalf("waiting %+v, transcript %q; want the refusal said", m.waiting, m.rawText())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if len(m.waiting) != 1 || m.input.Value() != "" || strings.Count(m.rawText(), "error: the store is down") != 2 {
		t.Fatalf("waiting %+v, prompt %q, transcript %q; want the message kept and the refusal said", m.waiting, m.input.Value(), m.rawText())
	}
}

// pump hands the messages the program would get to m until done holds.
func pump(t *testing.T, m *localTUI, msgs <-chan tea.Msg, done func() bool) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !done() {
		select {
		case msg := <-msgs:
			m.Update(msg)
		case <-deadline:
			t.Fatalf("timed out; the transcript is %q", m.rawText())
		}
	}
}

// postTo has m post its messages on a channel the test reads.
func postTo(m *localTUI) <-chan tea.Msg {
	msgs := make(chan tea.Msg, 1024)
	m.post = func(msg tea.Msg) { msgs <- msg }
	return msgs
}

// TestLocalTUISendNowKeysOnAnIdleAgent: with nothing running, Ctrl+J
// starts a turn as Enter does, and the message is not sent to be read now.
func TestLocalTUISendNowKeysOnAnIdleAgent(t *testing.T) {
	m := newScriptedTUI(t, "steps:\n  - text: \"hi\"\n")
	msgs := postTo(m)
	typeLine(m, "hello", tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !m.running || len(m.waiting) != 0 {
		t.Fatalf("Ctrl+J on an idle agent: running %v, waiting %+v; want a turn", m.running, m.waiting)
	}
	pump(t, m, msgs, func() bool { return !m.running })
	var id string
	for _, e := range logOf(t, m.agent) {
		if e.Type == session.TypeUserMessage {
			id = e.ID
		}
	}
	if sentNow(t, m, id) {
		t.Error("a message that started a turn was sent to be read now")
	}
	if strings.Count(m.rawText(), "hello") != 1 {
		t.Errorf("transcript %q, want the prompt once", m.rawText())
	}
}

// A drive that ends after a later one took over leaves the later one
// running, and still says its error.
func TestLocalTUIAnEarlierDrivesEnd(t *testing.T) {
	m := newTestTUI(t)
	m.running, m.gen = true, 2
	m.Update(localTurnDoneMsg{gen: 1})
	if !m.running {
		t.Fatal("the end of an earlier drive stopped the later one")
	}
	m.Update(localTurnDoneMsg{gen: 1, err: errors.New("boom")})
	if !m.running || !strings.Contains(m.rawText(), "error: boom") {
		t.Fatalf("running %v, transcript %q; want the error and the later drive running", m.running, m.rawText())
	}
}

// A message sent as a turn ended starts the next turn: it leaves the
// waiting ones and shows as that turn's prompt.
func TestLocalTUIAMessageThatStartsTheNextTurn(t *testing.T) {
	m := newTestTUI(t)
	m.waiting = []waitingMessage{{id: "evt_late", text: "late"}}
	m.Update(localEventMsg{ev: localEvent{kind: localPrompt, id: "evt_late", text: "late"}})
	m.Update(localEventMsg{ev: localEvent{kind: localPrompt, id: "evt_late", text: "late"}})
	if len(m.waiting) != 0 || strings.Count(m.rawText(), "late") != 1 {
		t.Fatalf("waiting %+v, transcript %q; want the message shown once as a prompt", m.waiting, m.rawText())
	}
}

// TestLocalTUIMessageWhileACommandRunsEndToEnd drives the TUI over the
// scripted model and the host machine: a message typed while the agent's
// command runs waits above the prompt, and once the command returns the
// agent reads it at its next step, where the transcript shows it.
func TestLocalTUIMessageWhileACommandRunsEndToEnd(t *testing.T) {
	m := newScriptedTUI(t, `steps:
  - tool_calls:
      - name: bash
        input: {command: "`+gateCommand+`"}
  - expect: {text_contains: "use port 9000"}
    text: "Using port 9000."
`)
	msgs := postTo(m)
	typeLine(m, "start the server", tea.KeyMsg{Type: tea.KeyEnter})
	pump(t, m, msgs, func() bool {
		return slices.ContainsFunc(m.blocks, func(b block) bool { return b.kind == blkTool })
	})
	typeLine(m, "use port 9000", tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.waiting) != 1 || lineOf(frameLines(m), "reads this after its current step") < 0 {
		t.Fatalf("waiting = %+v, want the message above the prompt", m.waiting)
	}
	openGate(t, m.agent)
	pump(t, m, msgs, func() bool { return !m.running })
	if len(m.waiting) != 0 {
		t.Fatalf("waiting = %+v after the turn", m.waiting)
	}
	var kinds []blockKind
	var users []string
	for _, b := range m.blocks {
		kinds = append(kinds, b.kind)
		if b.kind == blkUser {
			users = append(users, b.text)
		}
	}
	want := []blockKind{blkUser, blkTool, blkResult, blkUser, blkAssistant, blkNotice}
	if !slices.Equal(kinds, want) || !slices.Equal(users, []string{"start the server", "use port 9000"}) {
		t.Fatalf("blocks %v with prompts %q; want the message after the command's result", kinds, users)
	}
	if strings.Contains(m.rawText(), "error:") {
		t.Fatalf("the turn failed: %q", m.rawText())
	}
}
