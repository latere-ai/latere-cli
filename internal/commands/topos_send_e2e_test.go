// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
)

func TestTUISendAfterReconnectE2E(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     tea.KeyMsg
		control attachControl
	}{
		{"message", tea.KeyMsg{Type: tea.KeyEnter}, attachControl{Type: "user_turn", Text: "hello"}},
		{"approve", keyRunes("y"), attachControl{Type: "approval_reply", DecisionID: "d1", Approve: true}},
		{"deny", keyRunes("n"), attachControl{Type: "approval_reply", DecisionID: "d1"}},
		{"interrupt", tea.KeyMsg{Type: tea.KeyEsc}, attachControl{Type: "interrupt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			drop := make(chan struct{})
			resume := make(chan struct{})
			received := make(chan attachControl, 1)
			var connections atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				first := connections.Add(1) == 1
				frame := attachFrame{Type: "status", State: "running"}
				if tc.control.Type == "approval_reply" {
					frame.State = "awaiting_approval"
					if first {
						frame = ev("ApprovalRequest", `{"decision_id":"d1","tool_id":"bash"}`)
					}
				}
				data, _ := json.Marshal(frame)
				if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
					t.Error(err)
					return
				}
				if first {
					select {
					case <-drop:
					case <-ctx.Done():
					}
					return
				}
				_, data, err = conn.Read(ctx)
				if err != nil {
					return // Test cleanup may close an idle connection.
				}
				var control attachControl
				if err := json.Unmarshal(data, &control); err != nil {
					t.Error(err)
					return
				}
				received <- control
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			dials := 0
			stream := newFrameStream(ctx, func(ctx context.Context, since int64) (*attachConn, error) {
				dials++
				if dials > 1 {
					select {
					case <-resume:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return dialAttach(ctx, server.URL, "test-token", "sess_test", since, false)
			})
			defer stream.Close()
			m := newTUIModel("sess_test", stream.Events(), stream, false)
			consumeUntil := func(predicate func(streamMsg) bool) {
				t.Helper()
				drainUntil(t, stream, func(message streamMsg) bool {
					updated, _ := m.Update(streamEventMsg{ok: true, m: message})
					m = updated.(tuiModel)
					return predicate(message)
				})
			}
			consumeUntil(func(message streamMsg) bool { return message.frame != nil })
			close(drop)
			consumeUntil(func(message streamMsg) bool { return message.note == "reconnecting" })
			m.input.SetValue("hello")
			updated, _ := m.Update(tc.key)
			m = updated.(tuiModel)
			if !strings.Contains(m.View(), "not connected") || m.input.Value() != "hello" {
				t.Errorf("failed send lost input or diagnostic: %s", m.View())
			}
			if tc.control.Type == "approval_reply" && m.state.pending == nil {
				t.Error("failed send lost the pending approval")
			}
			close(resume)
			consumeUntil(func(message streamMsg) bool { return message.frame != nil })
			updated, _ = m.Update(tc.key)
			m = updated.(tuiModel)
			if tc.control.Type == "user_turn" && m.input.Value() != "" {
				t.Error("successful retry did not clear the message")
			}
			if tc.control.Type == "approval_reply" && m.state.pending != nil {
				t.Error("successful retry did not clear the approval")
			}
			select {
			case control := <-received:
				if control != tc.control {
					t.Errorf("server received %+v, want %+v", control, tc.control)
				}
			case <-ctx.Done():
				t.Fatal("retry did not reach the server")
			}
		})
	}
}
