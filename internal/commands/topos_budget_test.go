// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestBudgetBreachStopsPrintAndUI(t *testing.T) {
	for _, tc := range []struct{ name, payload, want string }{
		{"spend", `{"leg":"usd","actual_usd":2.5,"limit_usd":2}`, "budget limit reached: $2.5 spent (limit $2)"},
		{"small spend", `{"leg":"usd","actual_usd":0.0005,"limit_usd":0.0001}`, "budget limit reached: $0.0005 spent (limit $0.0001)"},
		{"missing details", `{}`, "budget limit reached"},
		{"missing actual", `{"leg":"usd","limit_usd":2}`, "budget limit reached"},
		{"different limit", `{"leg":"tokens","actual_usd":100,"limit_usd":90}`, "budget limit reached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := ev("BudgetBreach", tc.payload)
			conn := &fakePrintConn{frames: make(chan attachFrame, 2)}
			conn.frames <- frame
			conn.frames <- ev("Stop", `{}`)
			var out, diagnostic bytes.Buffer
			if err := streamPrint(t.Context(), conn, &out, &diagnostic, ""); err == nil || err.Error() != tc.want {
				t.Errorf("budget stop not reported: %v", err)
			}
			if len(conn.frames) != 1 || out.Len() != 0 || diagnostic.Len() != 0 {
				t.Error("budget stop consumed completion or wrote duplicate output")
			}
			model, _ := newTestModel(false)
			model.state.apply(ev("TextDelta", `{"text":"unfinished"}`))
			model.state.apply(ev("ApprovalRequest", `{"decision_id":"d1","tool_id":"bash"}`))
			updated, _ := model.Update(streamEventMsg{ok: true, m: streamMsg{frame: &frame}})
			model = updated.(tuiModel)
			if !strings.Contains(model.View(), tc.want) || model.state.liveText() != "" || model.state.pending != nil || model.state.status != "ready" {
				t.Errorf("UI hid the budget stop or retained in-flight state: %s", model.View())
			}
			model.state.apply(ev("Stop", `{"stop_reason":"budget_exceeded"}`))
			if strings.Count(model.View(), "budget limit reached") != 1 {
				t.Error("Stop duplicated the breach notice")
			}
		})
	}
}

func TestPrintRejectsMalformedStopPayload(t *testing.T) {
	for _, payload := range []string{`{`, `[]`, `{"stop_reason":false}`} {
		var out, diagnostic bytes.Buffer
		_, err := handlePrintFrame(ev("Stop", payload), &out, &diagnostic)
		if err == nil || !strings.Contains(err.Error(), "decode Stop payload") {
			t.Errorf("malformed stop reported success: %v", err)
		}
	}
}

func TestPrintIgnoresBudgetBreachFromEarlierTurn(t *testing.T) {
	conn := &fakePrintConn{frames: make(chan attachFrame, 3)}
	conn.frames <- ev("BudgetBreach", `{"leg":"usd","actual_usd":2.5,"limit_usd":2}`)
	conn.frames <- attachFrame{Type: "caught_up"}
	conn.frames <- ev("Stop", `{}`)
	var out, diagnostic bytes.Buffer
	if err := streamPrint(t.Context(), conn, &out, &diagnostic, "new prompt"); err != nil || diagnostic.Len() != 0 {
		t.Fatalf("historical breach affected a new turn: %v: %s", err, diagnostic.String())
	}
}

func TestStopReportsExistingBudgetLimit(t *testing.T) {
	frame := ev("Stop", `{"stop_reason":"budget_exceeded"}`)
	var out, diagnostic bytes.Buffer
	if done, err := handlePrintFrame(frame, &out, &diagnostic); !done || err == nil || err.Error() != "budget limit reached" {
		t.Errorf("budget stop reported success: done=%t err=%v", done, err)
	}
	model, _ := newTestModel(false)
	model.state.apply(frame)
	if !strings.Contains(model.View(), "budget limit reached") {
		t.Errorf("existing budget limit hidden in UI: %s", model.View())
	}
	model.state.apply(frame)
	if strings.Count(model.View(), "budget limit reached") != 1 {
		t.Error("budget stop reported twice for the same turn")
	}
	model.state.apply(ev("SessionStatus", `{"status":"running"}`))
	model.state.apply(frame)
	if strings.Count(model.View(), "budget limit reached") != 2 {
		t.Error("later capped turn was not reported")
	}
}
