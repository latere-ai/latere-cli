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

func TestTUIPartialTurnE2E(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "failed"
		if interrupted {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				send := func(frame attachFrame) bool {
					data, _ := json.Marshal(frame)
					if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
						t.Error(err)
						return false
					}
					return true
				}
				read := func(want attachControl) bool {
					_, data, err := conn.Read(ctx)
					var got attachControl
					if err != nil || json.Unmarshal(data, &got) != nil || got != want {
						t.Errorf("control = %+v, error = %v; want %+v", got, err, want)
						return false
					}
					return true
				}
				if !send(attachFrame{Type: "caught_up"}) || !send(attachFrame{
					Type: "event", Event: "TextDelta", Ephemeral: true, Payload: []byte(`{"text":"unfinished answer"}`),
				}) {
					return
				}
				if interrupted {
					if !read(attachControl{Type: "interrupt"}) {
						return
					}
				} else if !send(ev("RunError", `{"error":"model offline"}`)) {
					return
				}
				idle := ev("SessionStatus", `{"status":"awaiting_input"}`)
				idle.Seq = 2
				if !send(idle) || !read(attachControl{Type: "user_turn", Text: "try again"}) {
					return
				}
				if !send(attachFrame{Type: "event", Event: "TextDelta", Ephemeral: true, Payload: []byte(`{"text":"fresh answer"}`)}) {
					return
				}
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			stream := newFrameStream(ctx, func(ctx context.Context, since int64) (*attachConn, error) {
				return dialAttach(ctx, server.URL, "test-token", "sess_test", since, false)
			})
			defer stream.Close()
			model := newTUIModel("sess_test", stream.Events(), stream, false)
			receive := func(event string) {
				drainUntil(t, stream, func(message streamMsg) bool {
					updated, _ := model.Update(streamEventMsg{ok: true, m: message})
					model = updated.(tuiModel)
					return message.frame != nil && message.frame.Event == event
				})
			}
			receive("TextDelta")
			if !strings.Contains(model.View(), "● unfinished answer") {
				t.Fatal("initial partial response was not rendered")
			}
			if interrupted {
				updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
				model = updated.(tuiModel)
			}
			receive("SessionStatus")
			if !strings.Contains(model.View(), "unfinished answer (incomplete)") || !strings.Contains(model.View(), "[ready]") {
				t.Errorf("abandoned turn not finalized: %s", model.View())
			}
			if !interrupted && !strings.Contains(model.View(), "model offline") {
				t.Error("run error was lost")
			}
			updated, _ := model.Update(keyRunes("try again"))
			model = updated.(tuiModel)
			updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = updated.(tuiModel)
			receive("TextDelta")
			view := model.View()
			if !strings.Contains(view, "● fresh answer") || strings.Count(view, "unfinished answer") != 1 {
				t.Errorf("partial text leaked into the next response: %s", view)
			}
		})
	}
}
