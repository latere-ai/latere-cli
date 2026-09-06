// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"strings"
	"testing"
)

func TestSessionStateReportsRestoredSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
	}{
		{"saved turn", `{"from_turn":3,"message":"resumed from the last snapshot; any interrupted turn was rolled back"}`, "saved turn 3"},
		{"initial snapshot", `{"from_turn":0,"message":"resumed from the last snapshot; any interrupted turn was rolled back"}`, "saved turn 0"},
		{"missing details", `{}`, "session resumed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSessionState()
			s.apply(ev("AssistantMessage", `{"text":"completed work"}`))
			s.apply(ev("TextDelta", `{"text":"interrupted work"}`))
			s.apply(ev("ApprovalRequest", `{"decision_id":"old-decision","tool_id":"bash"}`))
			s.apply(ev("SessionResumed", tc.payload))
			text := strings.Join(s.lines, "\n")
			for _, want := range []string{tc.want, "rolled back", "completed work", "interrupted work (incomplete)"} {
				if !strings.Contains(text, want) {
					t.Errorf("restoration transcript missing %q: %s", want, text)
				}
			}
			if tc.name == "missing details" && strings.Contains(text, "saved turn 0") {
				t.Error("missing snapshot number was displayed as turn zero")
			}
			if s.pending != nil || s.liveText() != "" || s.status != "ready" {
				t.Errorf("stale turn state after restoration: pending=%v live=%q status=%q", s.pending, s.liveText(), s.status)
			}
			s.apply(ev("TextDelta", `{"text":"new response"}`))
			if s.liveText() != "new response" {
				t.Errorf("restored turn mixed with interrupted text: %q", s.liveText())
			}
		})
	}
}
