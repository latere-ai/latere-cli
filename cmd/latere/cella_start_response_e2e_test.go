// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCellaStartResponseE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	tokenPath := filepath.Join(root, "token.json")
	if err := os.WriteFile(tokenPath, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, field, output string
		args                []string
		follow              bool
	}{
		{"background", "command_id", "cmd-123\n", []string{"run", "dev"}, false},
		{"exec", "command_id", "output\n", []string{"exec", "dev"}, true},
		{"run follow", "command_id", "output\n", []string{"run", "dev", "--follow"}, true},
		{"detached", "run_id", "run-123\n", []string{"run", "--ephemeral", "--rm", "--detach"}, false},
		{"detached JSON", "run_id", `"run_id": "run-123"`, []string{"run", "--ephemeral", "--rm", "--detach", "--json"}, false},
	} {
		for _, response := range []struct{ name, body string }{
			{"missing", `{}`}, {"empty", `{"command_id":"","run_id":""}`},
			{"null", `null`}, {"no content", ""},
			{"valid", `{"command_id":"cmd-123","run_id":"run-123","state":"creating"}`},
		} {
			t.Run(tc.name+"/"+response.name, func(t *testing.T) {
				var starts, logs atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						starts.Add(1)
						if response.body == "" {
							w.WriteHeader(http.StatusNoContent)
							return
						}
						_, _ = w.Write([]byte(response.body))
						return
					}
					logs.Add(1)
					if r.URL.Path != "/v1/sandboxes/dev/commands/cmd-123/logs" {
						t.Errorf("invalid log request: %s", r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"bytes":"output\n","next_cursor":7,"phase":"exited","exit_code":0}`))
				}))
				defer server.Close()
				args := append([]string{"cella"}, tc.args...)
				args = append(args, "--api-url", server.URL, "--", "echo")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				wantLogs := int32(0)
				if response.name == "valid" {
					if err != nil || !strings.Contains(stdout.String(), tc.output) {
						t.Errorf("valid start = %v, stdout=%q, stderr=%q", err, stdout.String(), stderr.String())
					}
					if tc.follow {
						wantLogs = 1
					}
				} else {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "missing "+tc.field) {
						t.Errorf("incomplete response returned %v: %s", err, stderr.String())
					}
					if stdout.Len() != 0 || !strings.Contains(stderr.String(), "may have started") {
						t.Errorf("invalid acknowledgement: stdout=%q, stderr=%q", stdout.String(), stderr.String())
					}
				}
				if starts.Load() != 1 || logs.Load() != wantLogs {
					t.Errorf("start requests=%d, log requests=%d; want 1, %d", starts.Load(), logs.Load(), wantLogs)
				}
			})
		}
	}
}
