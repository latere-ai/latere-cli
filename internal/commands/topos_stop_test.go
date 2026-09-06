// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrintAndUIReportIncompleteStops(t *testing.T) {
	for _, tc := range []struct{ reason, message string }{
		{"", ""},
		{"end_turn", ""},
		{"stop_sequence", ""},
		{"max_tokens", "model output reached its token limit; response may be incomplete"},
		{"tool_use", "agent stopped while requesting tools; work may be incomplete"},
		{"budget_exceeded", "budget limit reached"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]string{"stop_reason": tc.reason})
			frame := ev("Stop", string(payload))
			var out, diagnostic bytes.Buffer
			done, err := handlePrintFrame(frame, &out, &diagnostic)
			if !done || (tc.message == "" && err != nil) || (tc.message != "" && (err == nil || err.Error() != tc.message)) {
				t.Errorf("stop outcome: done=%t err=%v, want message=%q", done, err, tc.message)
			}
			if out.Len() != 0 || diagnostic.Len() != 0 {
				t.Error("stop wrote duplicate output")
			}
			model, _ := newTestModel(false)
			model.state.apply(ev("AssistantMessage", `{"text":"partial answer"}`))
			model.state.status = "working"
			model.state.apply(frame)
			view := model.View()
			if !strings.Contains(view, "partial answer") || model.state.status != "ready" {
				t.Errorf("stop lost output or left the turn active: %s", view)
			}
			if tc.message == "" {
				if strings.Contains(view, "⚠") {
					t.Errorf("normal completion showed a warning: %s", view)
				}
			} else if !strings.Contains(view, tc.message) {
				t.Errorf("incomplete stop was hidden: %s", view)
			}
		})
	}
}
