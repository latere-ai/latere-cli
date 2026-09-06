// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestToposPrintReportsFailedStreamsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"start", "attach"} {
		for _, state := range []string{"completed", "closed session", "graceful disconnect", "abrupt disconnect", "protocol error", "error then stop", "empty error", "run error", "malformed answer", "malformed tool", "malformed tool failure", "malformed run error", "empty run error", "approval required", "invalid frame JSON", "invalid frame type", "invalid frame sequence", "null frame", "missing frame type", "trailing frame JSON", "malformed replay frame", "budget reached", "budget then stop", "malformed budget", "budget already exhausted", "token limit", "unfinished tools", "natural stop", "stop sequence", "budget before answer", "budget before answer idle"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				wantError, wantOutput := "", ""
				var frames []string
				if operation == "attach" {
					frames = append(frames,
						`{"type":"event","event":"AssistantMessage","seq":1,"payload":{"text":"old answer"}}`,
						`{"type":"event","event":"Stop","seq":2,"payload":{}}`,
						`{"type":"event","event":"RunError","seq":3,"payload":{"error":"old failure"}}`,
						`{"type":"event","event":"ApprovalRequest","seq":4,"payload":{"decision_id":"old-decision","tool_id":"old-tool"}}`,
						`{"type":"event","event":"PostToolUse","seq":5,"payload":{"tool_call":{"name":"old-tool"},"result":{}}}`,
					)
				}
				frames = append(frames, `{"type":"caught_up","seq":5}`)
				switch state {
				case "budget before answer", "budget before answer idle":
					wantError, wantOutput = "budget limit reached: $2.5 spent (limit $2)", "final partial answer\n"
					frames = append(frames,
						`{"type":"event","event":"BudgetBreach","seq":6,"payload":{"leg":"usd","actual_usd":2.5,"limit_usd":2}}`,
						`{"type":"event","event":"Usage","seq":7,"payload":{"total":{"input_tokens":10,"output_tokens":10}}}`,
						`{"type":"event","event":"AssistantMessage","seq":8,"payload":{"text":"final partial answer"}}`)
					if state == "budget before answer idle" {
						frames = append(frames, `{"type":"event","event":"SessionStatus","seq":9,"payload":{"status":"awaiting_input"}}`)
					} else {
						frames = append(frames, `{"type":"event","event":"Stop","seq":9,"payload":{"stop_reason":"budget_exceeded"}}`)
					}
				case "token limit", "unfinished tools", "natural stop", "stop sequence":
					reason := "end_turn"
					switch state {
					case "token limit":
						reason, wantError = "max_tokens", "model output reached its token limit; response may be incomplete"
					case "unfinished tools":
						reason, wantError = "tool_use", "agent stopped while requesting tools; work may be incomplete"
					case "stop sequence":
						reason = "stop_sequence"
					}
					wantOutput = "partial answer\n"
					frames = append(frames, `{"type":"event","event":"AssistantMessage","seq":6,"payload":{"text":"partial answer"}}`,
						`{"type":"event","event":"Stop","seq":7,"payload":{"stop_reason":"`+reason+`"}}`)
				case "budget already exhausted":
					wantError = "budget limit reached"
					frames = append(frames, `{"type":"event","event":"Stop","seq":6,"payload":{"stop_reason":"budget_exceeded"}}`)
				case "budget reached", "budget then stop", "malformed budget":
					wantError = "budget limit reached: $2.5 spent (limit $2)"
					payload := `{"leg":"usd","actual_usd":2.5,"limit_usd":2}`
					if state == "malformed budget" {
						wantError = "decode BudgetBreach payload"
						payload = `{"limit_usd":false}`
					}
					wantOutput = "partial answer\n"
					frames = append(frames, `{"type":"event","event":"AssistantMessage","seq":6,"payload":{"text":"partial answer"}}`,
						`{"type":"event","event":"BudgetBreach","seq":7,"payload":`+payload+`}`)
					if state == "budget then stop" || state == "malformed budget" {
						frames = append(frames, `{"type":"event","event":"Stop","seq":8,"payload":{}}`)
					} else {
						frames = append(frames, `{"type":"event","event":"SessionStatus","seq":8,"payload":{"status":"awaiting_input"}}`)
					}
				case "invalid frame JSON", "invalid frame type", "invalid frame sequence", "null frame", "missing frame type", "trailing frame JSON", "malformed replay frame":
					wantError = "decode session frame"
					malformed := `{`
					switch state {
					case "invalid frame type":
						malformed = `{"type":42}`
					case "invalid frame sequence":
						malformed = `{"type":"event","seq":"bad","event":"AssistantMessage","payload":{"text":"lost answer"}}`
					case "null frame":
						malformed = `null`
					case "missing frame type":
						malformed = `{"event":"AssistantMessage","payload":{"text":"lost answer"}}`
					case "trailing frame JSON":
						malformed = `{"type":"event","event":"Stop"} {}`
					}
					if state == "malformed replay frame" {
						frames = append([]string{malformed}, frames...)
					} else {
						wantOutput = "partial answer\n"
						frames = append(frames, `{"type":"event","event":"AssistantMessage","seq":6,"payload":{"text":"partial answer"}}`, malformed)
					}
					frames = append(frames, `{"type":"event","event":"Stop","seq":7,"payload":{}}`)
				case "completed", "graceful disconnect", "abrupt disconnect":
					wantOutput = "partial answer\n"
					frames = append(frames, `{"type":"event","event":"AssistantMessage","seq":6,"payload":{"text":"partial answer"}}`)
					if state == "completed" {
						frames = append(frames, `{"type":"event","event":"Stop","seq":7,"payload":{}}`)
					} else {
						wantError = "session disconnected before turn completed"
					}
				case "closed session":
					frames = append(frames, `{"type":"status","state":"closed"}`)
				case "protocol error", "error then stop":
					wantError = "session rejected prompt"
					frames = append(frames, `{"type":"error","message":"session rejected prompt"}`)
					if state == "error then stop" {
						frames = append(frames, `{"type":"event","event":"Stop","seq":7,"payload":{}}`)
					}
				case "empty error":
					wantError = "session protocol error"
					frames = append(frames, `{"type":"error"}`)
				case "run error":
					wantError = "agent unavailable"
					frames = append(frames, `{"type":"event","event":"RunError","seq":6,"payload":{"error":"agent unavailable"}}`)
				case "malformed answer", "malformed tool", "malformed tool failure", "malformed run error":
					event, payload := "AssistantMessage", `{"text":42}`
					switch state {
					case "malformed tool":
						event, payload = "PostToolUse", `{"tool_call":false}`
					case "malformed tool failure":
						event, payload = "PostToolUseFailure", `{"tool_call":false}`
					case "malformed run error":
						event, payload = "RunError", `{"error":42}`
					}
					wantError = "decode " + event + " payload"
					frame, _ := json.Marshal(map[string]any{"type": "event", "event": event, "seq": 6, "payload": json.RawMessage(payload)})
					frames = append(frames, string(frame), `{"type":"event","event":"Stop","seq":7,"payload":{}}`)
				case "approval required":
					wantError = "approval required"
					frames = append(frames, `{"type":"event","event":"ApprovalRequest","seq":6,"payload":{"decision_id":"decision-1","tool_id":"test-tool","args":{}}}`)
				case "empty run error":
					wantError = "agent reported an error"
					frames = append(frames, `{"type":"event","event":"RunError","seq":6,"payload":{}}`)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/sessions" {
						_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess_test", "status": "running"})
						return
					}
					if r.URL.Path != "/v1/sessions/sess_test/attach" || r.Header.Get("Authorization") != "Bearer test-topos" {
						t.Error("unexpected attach request")
					}
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.CloseNow() }()
					if operation == "attach" {
						if _, _, err := conn.Read(r.Context()); err != nil {
							t.Error(err)
							return
						}
					}
					for _, frame := range frames {
						if err := conn.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
							return // A fatal frame can make the CLI disconnect immediately.
						}
					}
					if state == "budget reached" || state == "budget before answer idle" {
						_, _, _ = conn.Read(r.Context()) // Session remains attached after a cap stop.
						return
					}
					if state == "approval required" {
						// The server remains blocked on the decision. Print mode must
						// disconnect without approving or denying on the user's behalf.
						if _, data, err := conn.Read(r.Context()); err == nil {
							t.Errorf("print mode sent an approval decision: %s", data)
						}
						return
					}
					if state != "abrupt disconnect" {
						_ = conn.Close(websocket.StatusNormalClosure, "done")
					}
				}))
				defer server.Close()
				argument := "sess_test"
				if operation == "start" {
					argument = "test-agent"
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "topos", "session", operation, argument, "-p", "test prompt", "--api-url", server.URL)
				command.Env = append(os.Environ(), "TOPOS_TOKEN=test-topos", "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if wantError != "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || strings.Count(stderr.String(), wantError) != 1 {
						t.Errorf("failed stream exit=%v, stderr=%q, want %q once", err, stderr.String(), wantError)
					}
				} else if err != nil || stderr.Len() != 0 {
					t.Errorf("completed stream failed: %v: %s", err, stderr.String())
				}
				if state == "approval required" && !strings.Contains(stderr.String(), "attach without --print") {
					t.Errorf("approval error lacks recovery instructions: %s", stderr.String())
				}
				if stdout.String() != wantOutput {
					t.Errorf("stdout=%q, want %q", stdout.String(), wantOutput)
				}
			})
		}
	}
}
