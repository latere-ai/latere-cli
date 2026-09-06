// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
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
			if len(conn.frames) != 0 || out.Len() != 0 || diagnostic.Len() != 0 {
				t.Error("budget stop did not finish the turn or wrote duplicate output")
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

func TestBudgetBreachPreservesFollowingOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  attachFrame
	}{
		{"budget stop", ev("Stop", `{"stop_reason":"budget_exceeded"}`)},
		{"plain stop", ev("Stop", `{}`)},
		{"idle event", ev("SessionStatus", `{"status":"awaiting_input"}`)},
		{"idle frame", attachFrame{Type: "status", State: "awaiting_input"}},
		{"closed frame", attachFrame{Type: "status", State: "closed"}},
		{"disconnect", attachFrame{}},
		{"malformed idle", ev("SessionStatus", `{"status":false}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &fakePrintConn{frames: make(chan attachFrame, 4)}
			conn.frames <- ev("BudgetBreach", `{"leg":"usd","actual_usd":2.5,"limit_usd":2}`)
			conn.frames <- ev("Usage", `{"total":{"input_tokens":10,"output_tokens":10}}`)
			conn.frames <- ev("AssistantMessage", `{"text":"final partial answer"}`)
			if tc.end.Type != "" {
				conn.frames <- tc.end
			}
			close(conn.frames)
			var out, diagnostic bytes.Buffer
			err := streamPrint(t.Context(), conn, &out, &diagnostic, "")
			if err == nil || !strings.Contains(err.Error(), "budget limit reached: $2.5 spent (limit $2)") || out.String() != "final partial answer\n" || diagnostic.Len() != 0 {
				t.Errorf("lost final answer or budget outcome: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
			}
			if tc.name == "disconnect" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("disconnect was hidden: %v", err)
			}
			if tc.name == "malformed idle" && (err == nil || !strings.Contains(err.Error(), "decode SessionStatus payload")) {
				t.Errorf("malformed status was hidden: %v", err)
			}
		})
	}
}

type cancelAfterPrint struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelAfterPrint) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	w.cancel()
	return n, err
}

func TestBudgetBreachPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn := &fakePrintConn{frames: make(chan attachFrame, 2)}
	conn.frames <- ev("BudgetBreach", `{}`)
	conn.frames <- ev("AssistantMessage", `{"text":"partial answer"}`)
	out := &cancelAfterPrint{cancel: cancel}
	var diagnostic bytes.Buffer
	err := streamPrint(ctx, conn, out, &diagnostic, "")
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "budget limit reached") || out.String() != "partial answer\n" {
		t.Errorf("cancellation lost budget context or output: err=%v out=%q", err, out.String())
	}
}

func TestBudgetBreachPreservesLaterWriteError(t *testing.T) {
	conn := &fakePrintConn{frames: make(chan attachFrame, 3)}
	conn.frames <- ev("BudgetBreach", `{}`)
	conn.frames <- ev("AssistantMessage", `{"text":"final partial answer"}`)
	conn.frames <- ev("Stop", `{}`)
	writeErr := errors.New("output full")
	var diagnostic bytes.Buffer
	err := streamPrint(t.Context(), conn, failingPrintWriter{err: writeErr}, &diagnostic, "")
	if !errors.Is(err, writeErr) || !strings.Contains(err.Error(), "budget limit reached") || len(conn.frames) != 1 {
		t.Errorf("write error or budget outcome lost: %v", err)
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
