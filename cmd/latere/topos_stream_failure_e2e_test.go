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
		for _, state := range []string{"completed", "closed session", "graceful disconnect", "abrupt disconnect", "protocol error", "error then stop", "empty error", "run error"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				wantError, wantOutput := "", ""
				frames := []string{`{"type":"caught_up","seq":0}`}
				switch state {
				case "completed", "graceful disconnect", "abrupt disconnect":
					wantOutput = "partial answer\n"
					frames = append(frames, `{"type":"event","event":"AssistantMessage","seq":1,"payload":{"text":"partial answer"}}`)
					if state == "completed" {
						frames = append(frames, `{"type":"event","event":"Stop","seq":2,"payload":{}}`)
					} else {
						wantError = "session disconnected before turn completed"
					}
				case "closed session":
					frames = append(frames, `{"type":"status","state":"closed"}`)
				case "protocol error", "error then stop":
					wantError = "session rejected prompt"
					frames = append(frames, `{"type":"error","message":"session rejected prompt"}`)
					if state == "error then stop" {
						frames = append(frames, `{"type":"event","event":"Stop","seq":2,"payload":{}}`)
					}
				case "empty error":
					wantError = "session protocol error"
					frames = append(frames, `{"type":"error"}`)
				case "run error":
					wantError = "agent unavailable"
					frames = append(frames, `{"type":"event","event":"RunError","seq":1,"payload":{"error":"agent unavailable"}}`)
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
				if stdout.String() != wantOutput {
					t.Errorf("stdout=%q, want %q", stdout.String(), wantOutput)
				}
			})
		}
	}
}
