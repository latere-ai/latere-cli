// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"strings"
	"testing"
)

func ev(event, payload string) attachFrame {
	return attachFrame{Type: "event", Event: event, Payload: []byte(payload), Seq: 1}
}

func TestSessionStateAssistantAndTools(t *testing.T) {
	s := newSessionState()

	s.apply(attachFrame{Type: "event", Event: "TextDelta", Payload: []byte(`{"text":"hel"}`), Ephemeral: true})
	s.apply(attachFrame{Type: "event", Event: "TextDelta", Payload: []byte(`{"text":"lo"}`), Ephemeral: true})
	if s.liveText() != "hello" {
		t.Fatalf("liveText = %q, want hello", s.liveText())
	}
	if s.status != "working" {
		t.Fatalf("status = %q, want working", s.status)
	}

	s.apply(ev("AssistantMessage", `{"text":"hello there"}`))
	if s.liveText() != "" {
		t.Fatal("live text should reset after AssistantMessage")
	}
	if len(s.lines) != 1 || !strings.Contains(s.lines[0], "hello there") {
		t.Fatalf("lines = %v, want the assistant message", s.lines)
	}

	s.apply(ev("PreToolUse", `{"tool_call":{"name":"bash"}}`))
	s.apply(ev("PostToolUse", `{"tool_call":{"name":"bash"},"result":{"content":"done\nok","is_error":false}}`))
	last := s.lines[len(s.lines)-1]
	if !strings.Contains(last, "bash") || !strings.Contains(last, "[ok]") {
		t.Fatalf("tool line = %q, want a bash ok line", last)
	}

	s.apply(ev("PostToolUseFailure", `{"tool_call":{"name":"bash"},"error":"denied by user"}`))
	if last := s.lines[len(s.lines)-1]; !strings.Contains(last, "bash") || !strings.Contains(last, "denied") {
		t.Fatalf("failure line = %q, want a denied/failed bash line", last)
	}

	s.apply(ev("Stop", `{}`))
	if s.status != "ready" {
		t.Fatalf("status after Stop = %q, want ready", s.status)
	}
}

func TestSessionStateApproval(t *testing.T) {
	s := newSessionState()
	s.apply(ev("ApprovalRequest", `{"decision_id":"d1","tool_id":"bash"}`))
	if s.pending == nil || s.pending.DecisionID != "d1" {
		t.Fatalf("pending = %+v, want decision d1", s.pending)
	}
	if s.status != "awaiting approval" {
		t.Fatalf("status = %q, want awaiting approval", s.status)
	}
	if id := s.resolvePending(); id != "d1" {
		t.Fatalf("resolvePending = %q, want d1", id)
	}
	if s.pending != nil {
		t.Fatal("pending should be cleared")
	}
	if id := s.resolvePending(); id != "" {
		t.Fatalf("resolvePending with none = %q, want empty", id)
	}
}

func TestSessionStateClearsFinishedApprovals(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		frame        attachFrame
		pending      bool
	}{
		{"running", "working", attachFrame{Type: "status", State: "running"}, false},
		{"idle", "ready", attachFrame{Type: "status", State: "awaiting_input"}, false},
		{"closed", "closed", attachFrame{Type: "status", State: "closed"}, false},
		{"still waiting", "awaiting approval", attachFrame{Type: "status", State: "awaiting_approval"}, true},
		{"empty status", "awaiting approval", attachFrame{Type: "status"}, true},
		{"completed", "ready", ev("Stop", `{}`), false},
		{"failed", "ready", ev("RunError", `{"error":"interrupted"}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSessionState()
			s.apply(ev("ApprovalRequest", `{"decision_id":"d1","tool_id":"test-tool"}`))
			s.apply(tc.frame)
			if (s.pending != nil) != tc.pending || s.status != tc.status {
				t.Fatalf("pending=%v, status=%q; want pending=%t, status=%q", s.pending, s.status, tc.pending, tc.status)
			}
		})
	}
}

func TestSessionStateErrorsAndStatus(t *testing.T) {
	s := newSessionState()
	s.apply(ev("RunError", `{"error":"model offline"}`))
	if !strings.Contains(s.lines[len(s.lines)-1], "model offline") {
		t.Fatalf("want a run error line, got %v", s.lines)
	}
	s.apply(attachFrame{Type: "error", Message: "bad frame"})
	if !strings.Contains(s.lines[len(s.lines)-1], "bad frame") {
		t.Fatal("want a protocol error line")
	}
	s.apply(attachFrame{Type: "status", State: "running"})
	if s.status != "working" {
		t.Fatalf("status = %q, want working", s.status)
	}
	// An empty-state status frame leaves the status unchanged.
	s.apply(attachFrame{Type: "status", State: ""})
	if s.status != "working" {
		t.Fatalf("empty status changed state to %q", s.status)
	}
	// An unrecognised frame type is a harmless no-op.
	s.apply(attachFrame{Type: "caught_up", Seq: 3})
	s.apply(attachFrame{Type: "status", State: "closed"})
	if s.status != "closed" {
		t.Fatalf("status = %q, want closed", s.status)
	}
}

func TestSessionStateTransitionsAfterServerStatus(t *testing.T) {
	for _, state := range []string{"running", "awaiting_approval"} {
		t.Run(state, func(t *testing.T) {
			s := newSessionState()
			if state == "awaiting_approval" {
				s.apply(ev("ApprovalRequest", `{"decision_id":"d1","tool_id":"bash"}`))
			}
			s.apply(attachFrame{Type: "status", State: state})
			if state == "awaiting_approval" {
				s.resolvePending()
				if s.status != "working" {
					t.Errorf("decided approval still waiting: %q", s.status)
				}
			}
			s.apply(ev("Stop", `{}`))
			if s.status != "ready" {
				t.Errorf("completed turn still active: %q", s.status)
			}
		})
	}
}

func TestSessionStateLastSeqTracksDurableOnly(t *testing.T) {
	s := newSessionState()
	s.apply(attachFrame{Type: "event", Event: "AssistantMessage", Payload: []byte(`{"text":"a"}`), Seq: 5})
	s.apply(attachFrame{Type: "event", Event: "TextDelta", Payload: []byte(`{"text":"x"}`), Seq: 99, Ephemeral: true})
	if s.lastSeq != 5 {
		t.Fatalf("lastSeq = %d, want 5 (ephemeral frames must not advance the cursor)", s.lastSeq)
	}
}

func TestSessionStateUsageAndSubagents(t *testing.T) {
	s := newSessionState()
	s.apply(ev("Usage", `{"total":{"input_tokens":120,"output_tokens":45}}`))
	if s.usage != "120 in / 45 out" {
		t.Fatalf("usage = %q, want '120 in / 45 out'", s.usage)
	}
	s.apply(ev("SubagentStart", `{"subagent_id":"run-1/sub/reviewer"}`))
	s.apply(ev("SubagentStop", `{"subagent_id":"run-1/sub/reviewer"}`))
	if len(s.lines) != 2 || !strings.Contains(s.lines[0], "started") || !strings.Contains(s.lines[1], "done") {
		t.Fatalf("sub-agent lines = %v", s.lines)
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("short"); got != "short" {
		t.Fatalf("shortID short = %q", got)
	}
	if got := shortID("run-1/sub/reviewer"); got != "…reviewer" {
		t.Fatalf("shortID = %q, want …reviewer", got)
	}
}

func TestEchoUserAndTruncLine(t *testing.T) {
	s := newSessionState()
	s.echoUser("hi there")
	if !strings.Contains(s.lines[0], "hi there") {
		t.Fatalf("echo = %v", s.lines)
	}
	if got := truncLine("a\nb  c", 100); got != "a b  c" {
		t.Fatalf("truncLine newline = %q", got)
	}
	if got := truncLine(strings.Repeat("x", 10), 3); got != "xxx…" {
		t.Fatalf("truncLine cap = %q, want xxx…", got)
	}
}
