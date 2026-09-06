// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
)

// Exercise replay, model rendering, keyboard handling, and the real control
// WebSocket together without requiring a platform-specific terminal driver.
func TestTUIApprovalLifecycleE2E(t *testing.T) {
	for _, tc := range []struct {
		name    string
		frame   attachFrame
		pending bool
		status  string
	}{
		{"resumed", attachFrame{Type: "status", State: "running"}, false, "working"},
		{"idle", attachFrame{Type: "status", State: "awaiting_input"}, false, "ready"},
		{"completed", ev("Stop", `{}`), false, "ready"},
		{"failed", ev("RunError", `{"error":"interrupted"}`), false, "ready"},
		{"still waiting", attachFrame{Type: "status", State: "awaiting_approval"}, true, "awaiting approval"},
		{"empty status", attachFrame{Type: "status"}, true, "awaiting approval"},
		{"live resumed", ev("SessionStatus", `{"session_id":"sess_test","status":"running"}`), false, "working"},
		{"live idle", ev("SessionStatus", `{"session_id":"sess_test","status":"awaiting_input"}`), false, "ready"},
		{"live waiting", ev("SessionStatus", `{"session_id":"sess_test","status":"awaiting_approval"}`), true, "awaiting approval"},
		{"budget stop", ev("BudgetBreach", `{"leg":"usd","actual_usd":2.5,"limit_usd":2}`), false, "ready"},
		{"token stop", ev("Stop", `{"stop_reason":"max_tokens"}`), false, "ready"},
		{"unfinished tools", ev("Stop", `{"stop_reason":"tool_use"}`), false, "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.frame.Type == "event" {
				tc.frame.Seq = 2
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			received := make(chan attachControl, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				for _, frame := range []attachFrame{
					ev("ApprovalRequest", `{"decision_id":"d1","tool_id":"test-tool"}`),
					{Type: "caught_up", Seq: 1},
					tc.frame,
				} {
					data, _ := json.Marshal(frame)
					if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
						t.Error(err)
						return
					}
				}
				_, data, err := conn.Read(ctx)
				var control attachControl
				if err != nil || json.Unmarshal(data, &control) != nil {
					t.Errorf("read UI control: %v: %s", err, data)
					return
				}
				received <- control
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"event","event":"Stop","seq":3,"payload":{}}`)); err != nil {
					t.Error(err)
					return
				}
				_, _, _ = conn.Read(ctx) // Keep the connection live until test cleanup.
			}))
			defer server.Close()
			stream := newFrameStream(ctx, func(ctx context.Context, since int64) (*attachConn, error) {
				return dialAttach(ctx, server.URL, "test-token", "sess_test", since, false)
			})
			defer stream.Close()
			model := newTUIModel("sess_test", stream.Events(), stream, false)
			frames := 0
			for frames < 3 {
				select {
				case message, ok := <-stream.Events():
					if !ok {
						t.Fatal("stream closed before replay completed")
					}
					updated, _ := model.Update(streamEventMsg{ok: true, m: message})
					model = updated.(tuiModel)
					if message.frame != nil {
						frames++
					}
				case <-ctx.Done():
					t.Fatal("timed out receiving replay")
				}
			}
			if strings.Contains(model.View(), "approve tool") != tc.pending {
				t.Errorf("stale approval prompt in view: %s", model.View())
			}
			if !strings.Contains(model.View(), "["+tc.status+"]") {
				t.Errorf("wrong status after replay: %s", model.View())
			}
			if tc.name == "budget stop" && !strings.Contains(model.View(), "budget limit reached") {
				t.Errorf("budget stop not explained: %s", model.View())
			}
			if (tc.name == "token stop" || tc.name == "unfinished tools") && !strings.Contains(model.View(), "may be incomplete") {
				t.Errorf("incomplete stop not explained: %s", model.View())
			}
			updated, _ := model.Update(keyRunes("y"))
			model = updated.(tuiModel)
			if tc.pending && !strings.Contains(model.View(), "[working]") {
				t.Errorf("decided approval still waiting: %s", model.View())
			}
			if !tc.pending {
				if model.input.Value() != "y" {
					t.Error("typing y was consumed as a stale approval")
				}
				updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
				model = updated.(tuiModel)
			}
			select {
			case control := <-received:
				if tc.pending {
					if control.Type != "approval_reply" || control.DecisionID != "d1" || !control.Approve {
						t.Errorf("active approval lost: %+v", control)
					}
				} else if control.Type != "user_turn" || control.Text != "y" {
					t.Errorf("stale approval sent to server: %+v", control)
				}
			case <-ctx.Done():
				t.Fatal("timed out receiving UI control")
			}
			drainUntil(t, stream, func(message streamMsg) bool {
				updated, _ := model.Update(streamEventMsg{ok: true, m: message})
				model = updated.(tuiModel)
				return message.frame != nil && message.frame.Event == "Stop"
			})
			if !strings.Contains(model.View(), "[ready]") {
				t.Errorf("completed turn still active: %s", model.View())
			}
		})
	}
}
