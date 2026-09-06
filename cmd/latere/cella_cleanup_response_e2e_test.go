// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestOneShotCleanupFailurePreservesOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	token := filepath.Join(root, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	output := strings.Repeat("result\n", 5000)
	for _, asJSON := range []bool{false, true} {
		for _, code := range []int{0, 7} {
			t.Run(fmt.Sprintf("json=%v/exit=%d", asJSON, code), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/v1/one-shot-runs" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "run-test", "sandbox_name": "test", "state": "cleanup_failed", "exit_code": code, "stdout": output, "stderr": "warning\n", "cleanup_error": "delete denied"})
				}))
				defer server.Close()
				args := []string{"cella", "run", "--ephemeral", "--rm", "--api-url", server.URL}
				if asJSON {
					args = append(args, "--json")
				}
				args = append(args, "--", "echo")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				wantExit := code
				if wantExit == 0 {
					wantExit = 1
				}
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != wantExit {
					t.Errorf("exit=%v, want %d", err, wantExit)
				}
				if asJSON {
					var result struct {
						Stdout, Stderr string
						CleanupError   string `json:"cleanup_error"`
						State          string
						ExitCode       int `json:"exit_code"`
					}
					if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
						t.Fatalf("missing JSON result: %v", err)
					}
					if result.Stdout != output || result.Stderr != "warning\n" || result.CleanupError != "delete denied" || result.State != "cleanup_failed" || result.ExitCode != code {
						t.Error("JSON result lost command output or cleanup failure")
					}
				} else {
					if stdout.String() != output {
						t.Errorf("stdout bytes=%d, want %d", stdout.Len(), len(output))
					}
					if !strings.HasPrefix(stderr.String(), "warning\n") || !strings.Contains(stderr.String(), "sandbox cleanup failed") || !strings.Contains(stderr.String(), "delete denied") || strings.Contains(stderr.String(), "cella deleted") {
						t.Errorf("cleanup diagnostic incorrect (%d bytes)", stderr.Len())
					}
				}
				if requests.Load() != 1 {
					t.Errorf("run requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
