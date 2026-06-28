// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"encoding/json"
	"fmt"
	"strings"
)

// sessionState is the pure, render-agnostic view of an attached session: it
// folds incoming frames into a transcript and status. It has no dependency on
// the terminal UI, so it is unit-testable on its own; the bubbletea model wraps
// it for display.
type sessionState struct {
	// lines is the rendered transcript (completed messages, tool lines, errors).
	lines []string
	// status summarises what the session is doing: "ready", "working",
	// "awaiting approval", "closed", or "disconnected".
	status string
	// pending is the outstanding approval request, if any.
	pending *approvalRequestPayload
	// lastSeq is the highest durable event seq seen, for reattach cursors.
	lastSeq int64
	// usage is the running token summary for the cost/usage HUD ("" until known).
	usage string
	// turn accumulates the in-flight assistant turn's streamed text.
	turn strings.Builder
}

func newSessionState() *sessionState {
	return &sessionState{status: "ready"}
}

// liveText returns the in-progress assistant turn's accumulated text (for live
// rendering before the assembled AssistantMessage arrives).
func (s *sessionState) liveText() string { return s.turn.String() }

// apply folds one frame into the state.
func (s *sessionState) apply(fr attachFrame) {
	if fr.Seq > s.lastSeq && !fr.Ephemeral {
		s.lastSeq = fr.Seq
	}
	switch fr.Type {
	case "status":
		if fr.State == "closed" {
			s.status = "closed"
		} else if fr.State != "" {
			s.status = fr.State
		}
	case "error":
		s.lines = append(s.lines, "⚠ "+fr.Message)
	case "event":
		s.applyEvent(fr)
	}
}

func (s *sessionState) applyEvent(fr attachFrame) {
	switch fr.Event {
	case "TextDelta":
		var p textDeltaPayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			s.turn.WriteString(p.Text)
			s.status = "working"
		}
	case "AssistantMessage":
		var p assistantMessagePayload
		if json.Unmarshal(fr.Payload, &p) == nil && p.Text != "" {
			s.lines = append(s.lines, "● "+p.Text)
		}
		s.turn.Reset()
	case "PreToolUse":
		var p preToolUsePayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			s.lines = append(s.lines, "→ "+p.ToolCall.Name)
		}
		s.status = "working"
	case "PostToolUse":
		var p postToolUsePayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			mark := "ok"
			if p.Result.IsError {
				mark = "error"
			}
			s.lines = append(s.lines, fmt.Sprintf("  %s [%s] %s", p.ToolCall.Name, mark, truncLine(p.Result.Content, 80)))
		}
	case "PostToolUseFailure":
		var p postToolUseFailurePayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			s.lines = append(s.lines, fmt.Sprintf("  %s [denied/failed] %s", p.ToolCall.Name, truncLine(p.Error, 80)))
		}
	case "ApprovalRequest":
		var p approvalRequestPayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			s.pending = &p
			s.status = "awaiting approval"
		}
	case "Usage":
		var p usagePayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			s.usage = fmt.Sprintf("%d in / %d out", p.Total.InputTokens, p.Total.OutputTokens)
		}
	case "SubagentStart":
		var p subagentPayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			s.lines = append(s.lines, "↳ sub-agent "+shortID(p.SubagentID)+" started")
		}
	case "SubagentStop":
		var p subagentPayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			s.lines = append(s.lines, "↳ sub-agent "+shortID(p.SubagentID)+" done")
		}
	case "RunError":
		var p runErrorPayload
		_ = json.Unmarshal(fr.Payload, &p)
		s.lines = append(s.lines, "⚠ run error: "+p.Error)
		s.status = "ready"
	case "Stop":
		// The turn completed; the in-flight buffer (if any) is now redundant.
		s.turn.Reset()
		if s.status == "working" {
			s.status = "ready"
		}
	}
}

// resolvePending clears and returns the outstanding approval's decision id, or
// "" if there is none.
func (s *sessionState) resolvePending() string {
	if s.pending == nil {
		return ""
	}
	id := s.pending.DecisionID
	s.pending = nil
	if s.status == "awaiting approval" {
		s.status = "working"
	}
	return id
}

// echoUser appends the user's own message to the transcript.
func (s *sessionState) echoUser(text string) {
	s.lines = append(s.lines, "› "+text)
}

// shortID trims a long id to its last 8 characters for compact display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return "…" + id[len(id)-8:]
}

// truncLine collapses content to a single trimmed line capped at n runes.
func truncLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
