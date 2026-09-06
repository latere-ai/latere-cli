// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestToposSessionOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	const detail = `{"session_id":"sess-1","output":"First line\nSecond line\n"}`
	const record = "session:    sess-1\nsandbox:    -\nstop_reason:-\ntool_calls: 0\ntokens:     0 in / 0 out\n"
	const row = "sess-1  awaiting_input    agent-1\n"
	for _, tc := range []struct {
		name                     string
		args                     []string
		method, path, body, want string
	}{
		{"create", []string{"session", "create", "agent-1", "--prompt", "test"}, "POST", "/v1/agents/agent-1/sessions", detail, record + "\nFirst line\nSecond line\n\n"},
		{"empty result", []string{"session", "create", "agent-1", "--prompt", "test"}, "POST", "/v1/agents/agent-1/sessions", `{"session_id":"sess-1"}`, record},
		{"list", []string{"session", "ls"}, "GET", "/v1/sessions", `{"sessions":[{"id":"sess-1","status":"awaiting_input","agent_id":"agent-1"},{"id":"sess-1","status":"awaiting_input","agent_id":"agent-1"}]}`, row + row},
		{"empty list", []string{"session", "ls"}, "GET", "/v1/sessions", `{"sessions":[]}`, "No interactive sessions.\n"},
	} {
		for _, writable := range []bool{true, false} {
			name := tc.name + "/read-only"
			if writable {
				name = tc.name + "/writable"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				output := filepath.Join(t.TempDir(), "output")
				if err := os.WriteFile(output, []byte("previous\n"), 0600); err != nil {
					t.Fatal(err)
				}
				flags := os.O_RDONLY
				if writable {
					flags = os.O_WRONLY | os.O_APPEND
				}
				file, err := os.OpenFile(output, flags, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				args := append([]string{"topos"}, tc.args...)
				command := exec.CommandContext(ctx, binary, append(args, "--api-url", server.URL)...)
				command.Env = append(os.Environ(), "TOPOS_TOKEN=synthetic-token", "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = file, &diagnostic
				err = command.Run()
				want := "previous\n"
				if writable {
					want += tc.want
					if err != nil || diagnostic.Len() != 0 {
						t.Errorf("output failed: %v %q", err, diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write session") {
					t.Errorf("ignored output failure: %v %q", err, diagnostic.String())
				}
				data, readErr := os.ReadFile(output)
				if readErr != nil || string(data) != want {
					t.Errorf("output=%q read=%v, want %q", data, readErr, want)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
