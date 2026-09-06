// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
)

func TestTUIApprovalDraftE2E(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}},
		{"typing", keyRunes("unseen edit")},
		{"paste", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true}},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			received := make(chan attachControl, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"event","event":"ApprovalRequest","payload":{"decision_id":"d1","tool_id":"bash"}}`)); err != nil {
					t.Error(err)
					return
				}
				for {
					_, data, err := conn.Read(ctx)
					if err != nil {
						return
					}
					var control attachControl
					if err := json.Unmarshal(data, &control); err != nil {
						t.Error(err)
						return
					}
					select {
					case received <- control:
					case <-ctx.Done():
						return
					}
				}
			}))
			defer server.Close()
			stream := newFrameStream(ctx, func(ctx context.Context, since int64) (*attachConn, error) {
				return dialAttach(ctx, server.URL, "test-token", "sess_test", since, false)
			})
			defer stream.Close()
			m := newTUIModel("sess_test", stream.Events(), stream, false)
			m.input.SetValue("my unfinished draft")
			drainUntil(t, stream, func(message streamMsg) bool {
				updated, _ := m.Update(streamEventMsg{ok: true, m: message})
				m = updated.(tuiModel)
				return m.state.pending != nil
			})
			// Keys pressed while the draft is hidden must not change or send it.
			for _, key := range []tea.KeyMsg{tc.key, keyRunes("y"), {Type: tea.KeyEnter}} {
				updated, _ := m.Update(key)
				m = updated.(tuiModel)
			}
			// The decision must precede the intact draft on the actual wire.
			for _, want := range []attachControl{
				{Type: "approval_reply", DecisionID: "d1", Approve: true},
				{Type: "user_turn", Text: "my unfinished draft"},
			} {
				select {
				case got := <-received:
					if got != want {
						t.Fatalf("received %+v, want %+v", got, want)
					}
				case <-ctx.Done():
					t.Fatal("timed out waiting for control message")
				}
			}
		})
	}
}
