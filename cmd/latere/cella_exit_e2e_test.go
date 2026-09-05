// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
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
)

func TestCellaCompletionExitStatusE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	tokenFile := filepath.Join(root, "token.json")
	if err := os.WriteFile(tokenFile, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	methods := map[string][]string{
		"exec":          {"cella", "exec", "dev", "--", "true"},
		"follow":        {"cella", "run", "dev", "--follow", "--", "true"},
		"wait":          {"cella", "wait", "dev", "cmd-test"},
		"logs":          {"cella", "logs", "dev", "cmd-test", "--follow"},
		"run_logs":      {"cella", "run", "logs", "run-test", "--follow"},
		"one_shot":      {"cella", "run", "--ephemeral", "--rm", "--", "true"},
		"one_shot_json": {"cella", "run", "--ephemeral", "--rm", "--json", "--", "true"},
	}
	for _, tc := range []struct {
		name, phase string
		code        *int
		want        int
	}{
		{"lost", "lost", nil, 1},
		{"cancelled", "cancelled", nil, 1},
		{"cleanup_failed", "cleanup_failed", new(0), 1},
		{"missing_status", "exited", nil, 1},
		{"failed_command", "exited", new(7), 7},
		{"invalid_status", "exited", new(256), 1},
		{"timeout", "timeout", new(-1), 1},
		{"success", "exited", new(0), 0},
	} {
		for name, args := range methods {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/commands") {
						_ = json.NewEncoder(w).Encode(map[string]any{"command_id": "cmd-test", "phase": "running"})
						return
					}
					result := map[string]any{"command_id": "cmd-test", "phase": tc.phase, "state": tc.phase, "bytes": "last output\n", "stdout": "last output\n", "next_cursor": 12}
					if tc.code != nil {
						result["exit_code"] = *tc.code
					}
					_ = json.NewEncoder(w).Encode(result)
				}))
				defer server.Close()
				// Environment overrides preserve argv after '--' while keeping every API
				// request local and avoiding update checks or ambient identity files.
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "SANDBOX_API_URL="+server.URL, "LATERE_TOKEN_FILE="+tokenFile, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				out, err := command.CombinedOutput()
				code := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatalf("run CLI: %v", err)
					}
					code = exit.ExitCode()
				}
				if code != tc.want {
					t.Errorf("exit status = %d, want %d; output: %s", code, tc.want, out)
				}
				if name != "wait" && !strings.Contains(string(out), "last output") {
					t.Errorf("final command output lost: %s", out)
				}
			})
		}
	}
}
