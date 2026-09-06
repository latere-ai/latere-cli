// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestToposStreamConfiguredOutput(t *testing.T) {
	t.Setenv("TOPOS_TOKEN", "synthetic-token")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, operation := range []string{"start", "attach"} {
		for _, event := range []struct{ name, payload, output string }{
			{"AssistantMessage", `{"text":"test answer"}`, "test answer\n"},
			{"PostToolUse", `{"tool_call":{"name":"test-tool"},"result":{}}`, "· test-tool [ok]\n"},
			{"PostToolUseFailure", `{"tool_call":{"name":"test-tool"}}`, "· test-tool [denied/failed]\n"},
		} {
			for _, failAfter := range []int{-1, 0, 3} {
				t.Run(fmt.Sprintf("%s/%s/failAfter=%d", operation, event.name, failAfter), func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Error("incorrect authorization")
						}
						if r.URL.Path == "/v1/sessions" {
							var body struct {
								AgentID string `json:"agent_id"`
								Prompt  string
							}
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != "POST" || body.AgentID != "agent-1" || body.Prompt != "test prompt" {
								t.Errorf("create: body=%+v err=%v", body, err)
							}
							_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess-1", "status": "running"})
							return
						}
						if r.URL.Path != "/v1/sessions/sess-1/attach" || r.Method != "GET" || (r.URL.Query().Get("mode") == "ro") != (operation == "start") {
							t.Errorf("attach=%s %s", r.Method, r.URL)
						}
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.CloseNow() }()
						if operation == "attach" {
							_, data, err := conn.Read(r.Context())
							var control attachControl
							if err != nil || json.Unmarshal(data, &control) != nil || control.Type != "user_turn" || control.Text != "test prompt" {
								t.Errorf("turn=%s err=%v", data, err)
								return
							}
						}
						frame, _ := json.Marshal(map[string]any{"type": "event", "event": event.name, "seq": 1, "payload": json.RawMessage(event.payload)})
						for _, data := range [][]byte{[]byte(`{"type":"caught_up","seq":0}`), frame, []byte(`{"type":"event","event":"Stop","seq":2,"payload":{}}`)} {
							if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
								return
							}
						}
						_ = conn.Close(websocket.StatusNormalClosure, "done")
					}))
					defer server.Close()
					sentinel := errors.New("stream output unavailable")
					out, diagnostic := &evalOutputWriter{}, &evalOutputWriter{}
					target, other := out, diagnostic
					if event.name != "AssistantMessage" {
						target, other = diagnostic, out
					}
					if failAfter >= 0 {
						target.remaining, target.err = failAfter, sentinel
					}
					root := NewRoot("test")
					root.SetOut(out)
					root.SetErr(diagnostic)
					argument := "sess-1"
					if operation == "start" {
						argument = "agent-1"
					}
					root.SetArgs([]string{"topos", "session", operation, argument, "-p", "test prompt", "--api-url", server.URL})
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					err := root.ExecuteContext(ctx)
					want := event.output
					if failAfter >= 0 {
						want = want[:failAfter]
						if !errors.Is(err, sentinel) {
							t.Errorf("output error=%v", err)
						}
					} else if err != nil {
						t.Errorf("stream: %v", err)
					}
					if target.String() != want || other.String() != "" {
						t.Errorf("target=%q other=%q, want target=%q", target.String(), other.String(), want)
					}
					expectedRequests := int32(1)
					if operation == "start" {
						expectedRequests++
					}
					if requests.Load() != expectedRequests {
						t.Errorf("requests=%d, want %d", requests.Load(), expectedRequests)
					}
				})
			}
		}
	}
}
