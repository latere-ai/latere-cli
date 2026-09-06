// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"strings"
	"testing"
)

func TestSessionStateFinishesAbandonedText(t *testing.T) {
	frames := []attachFrame{ev("RunError", `{"error":"model offline"}`)}
	for _, status := range []string{"awaiting_input", "completed", "failed", "closed"} {
		frames = append(frames, attachFrame{Type: "status", State: status},
			ev("SessionStatus", `{"status":"`+status+`"}`))
	}
	for _, frame := range frames {
		t.Run(frame.Type+"/"+frame.Event+frame.State+string(frame.Payload), func(t *testing.T) {
			s := newSessionState()
			s.apply(ev("AssistantMessage", `{"text":"earlier answer"}`))
			s.apply(ev("TextDelta", `{"text":"unfinished answer"}`))
			s.apply(frame)
			if s.liveText() != "" {
				t.Errorf("finished turn remains live: %q", s.liveText())
			}
			if len(s.lines) < 2 || s.lines[1] != "● unfinished answer (incomplete)" {
				t.Errorf("partial answer lost or unmarked: %v", s.lines)
			}
			s.apply(attachFrame{Type: "status", State: "awaiting_input"})
			if strings.Count(strings.Join(s.lines, "\n"), "unfinished answer") != 1 {
				t.Errorf("want exactly one retained partial answer: %v", s.lines)
			}
			s.apply(ev("TextDelta", `{"text":"next answer"}`))
			if s.liveText() != "next answer" {
				t.Errorf("previous turn leaked into next response: %q", s.liveText())
			}
		})
	}
}

func TestSessionStateKeepsActiveText(t *testing.T) {
	for _, status := range []string{"running", "awaiting_approval", "", "future_status"} {
		for _, frame := range []attachFrame{
			{Type: "status", State: status}, ev("SessionStatus", `{"status":"`+status+`"}`),
		} {
			t.Run(frame.Type+"/"+status, func(t *testing.T) {
				s := newSessionState()
				s.apply(ev("TextDelta", `{"text":"still streaming"}`))
				s.apply(frame)
				if s.liveText() != "still streaming" || len(s.lines) != 0 {
					t.Fatalf("active text finalized: live=%q lines=%v", s.liveText(), s.lines)
				}
			})
		}
	}
}
