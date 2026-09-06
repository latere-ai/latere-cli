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
	"sync/atomic"
	"testing"
	"time"
)

func TestOneShotRunOutputFailureE2E(t *testing.T) {
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
		name, failedStream string
		remoteExit, exit   int
	}{
		{name: "success"},
		{name: "remote failure", remoteExit: 7, exit: 7},
		{name: "stdout failure", failedStream: "stdout", exit: 1},
		{name: "stderr failure", failedStream: "stderr", exit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/one-shot-runs" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"state": "exited", "exit_code": tc.remoteExit, "sandbox_name": "test", "stdout": "result\n", "stderr": "warning\n"})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "cella", "run", "--api-url", server.URL, "--ephemeral", "--rm", "--", "echo")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if tc.failedStream != "" {
				path := filepath.Join(t.TempDir(), "read-only")
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				if tc.failedStream == "stdout" {
					command.Stdout = file
				} else {
					command.Stderr = file
				}
			}
			err := command.Run()
			if tc.exit == 0 {
				if err != nil {
					t.Errorf("successful run returned %v: %s", err, stderr.String())
				}
			} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != tc.exit {
				t.Errorf("run returned %v, want exit %d: %s", err, tc.exit, stderr.String())
			}
			if tc.failedStream == "stdout" {
				if !strings.Contains(stderr.String(), "write command stdout") || strings.Contains(stderr.String(), "✓") {
					t.Errorf("stdout failure diagnostic = %q", stderr.String())
				}
			} else if stdout.String() != "result\n" {
				t.Errorf("stdout = %q", stdout.String())
			}
			if tc.failedStream == "" && !strings.HasPrefix(stderr.String(), "warning\n") {
				t.Errorf("remote stderr lost: %q", stderr.String())
			}
			if requests.Load() != 1 {
				t.Errorf("run requests = %d, want 1", requests.Load())
			}
		})
	}
}
