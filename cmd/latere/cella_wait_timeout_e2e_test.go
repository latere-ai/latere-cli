// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCellaWaitTimeoutValidationE2E(t *testing.T) {
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
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, tc := range []struct {
			value string
			valid bool
		}{
			{"", true}, {"1", true}, {"1200", true}, {"9223372036", true},
			{"0", false}, {"-1", false}, {"9223372037", false}, {"18446744074", false}, {"9223372036854775807", false},
		} {
			t.Run(prefix+"/"+tc.value, func(t *testing.T) {
				if tc.value == "9223372036" && strconv.IntSize == 32 {
					t.Skip("timeout exceeds this platform's integer range")
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes/dev/commands/command" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"phase":"exited","exit_code":0}`))
				}))
				defer server.Close()
				args := []string{prefix, "wait", "dev", "command", "--api-url", server.URL}
				if tc.value != "" {
					args = append(args, "--timeout", tc.value)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				out, err := command.CombinedOutput()
				if tc.valid {
					if err != nil || requests.Load() != 1 || !strings.Contains(string(out), "phase=exited exit_code=0") {
						t.Errorf("valid timeout returned %v, requests=%d: %s", err, requests.Load(), out)
					}
				} else {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "--timeout") {
						t.Errorf("invalid timeout returned %v: %s", err, out)
					}
					if requests.Load() != 0 {
						t.Errorf("invalid timeout made %d requests", requests.Load())
					}
				}
			})
		}
	}
}
