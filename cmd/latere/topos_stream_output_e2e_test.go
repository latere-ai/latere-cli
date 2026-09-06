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
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/latere-ai/latere-cli/internal/commands"
)

func TestToposStreamConfiguredOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"start", "attach"} {
		for _, event := range []struct {
			name, payload, output string
		}{
			{"AssistantMessage", `{"text":"test answer"}`, "test answer\n"},
			{"PostToolUse", `{"tool_call":{"name":"test-tool"},"result":{}}`, "· test-tool [ok]\n"},
			{"PostToolUseFailure", `{"tool_call":{"name":"test-tool"}}`, "· test-tool [denied/failed]\n"},
		} {
			for _, mode := range []string{"writable", "read only"} {
				t.Run(operation+"/"+event.name+"/"+mode, func(t *testing.T) {
					root := t.TempDir()
					dest := filepath.Join(root, "output")
					const before = "existing output\n"
					if err := os.WriteFile(dest, []byte(before), 0600); err != nil {
						t.Fatal(err)
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/v1/sessions" {
							_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess_test", "status": "running"})
							return
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
						frame, _ := json.Marshal(map[string]any{"type": "event", "event": event.name, "seq": 1, "payload": json.RawMessage(event.payload)})
						for _, data := range [][]byte{[]byte(`{"type":"caught_up","seq":0}`), frame, []byte(`{"type":"event","event":"Stop","seq":2,"payload":{}}`)} {
							if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
								return
							}
						}
						_ = conn.Close(websocket.StatusNormalClosure, "done")
					}))
					defer server.Close()
					argument := "sess_test"
					if operation == "start" {
						argument = "test-agent"
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, "-test.run=^TestToposStreamOutputHelperProcess$", "--", "topos", "session", operation, argument, "-p", "test prompt", "--api-url", server.URL)
					command.Env = append(os.Environ(), "TOPOS_TOKEN=test-topos", "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root, "LATERE_TEST_STREAM_OUTPUT="+dest, "LATERE_TEST_STREAM_WRITABLE="+mode, "LATERE_TEST_STREAM_EVENT="+event.name)
					var output, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &output, &diagnostic
					err := command.Run()
					want := before + event.output
					if mode == "read only" {
						want = before
						if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
							t.Errorf("lost output reported success: %v: %s", err, diagnostic.String())
						}
					} else if err != nil {
						t.Errorf("writable output failed: %v: %s", err, diagnostic.String())
					}
					if output.Len() != 0 {
						t.Errorf("leaked stdout=%q", output.String())
					}
					if mode == "writable" && diagnostic.Len() != 0 {
						t.Errorf("leaked stderr=%q", diagnostic.String())
					}
					if data, err := os.ReadFile(dest); err != nil || string(data) != want {
						t.Errorf("output file=%q (%v), want %q", data, err, want)
					}
				})
			}
		}
	}
}

// Configure the command writers independently of the process descriptors.
func TestToposStreamOutputHelperProcess(t *testing.T) {
	path := os.Getenv("LATERE_TEST_STREAM_OUTPUT")
	if path == "" {
		return
	}
	flags := os.O_RDONLY
	if os.Getenv("LATERE_TEST_STREAM_WRITABLE") == "writable" {
		flags = os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		t.Fatal(err)
	}
	root := commands.NewRoot("test")
	if os.Getenv("LATERE_TEST_STREAM_EVENT") == "AssistantMessage" {
		root.SetOut(file)
	} else {
		root.SetErr(file)
	}
	root.SetArgs(os.Args[3:])
	err = root.Execute()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
	os.Exit(0)
}
