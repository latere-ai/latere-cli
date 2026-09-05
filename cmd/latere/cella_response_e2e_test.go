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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCellaCommandsRequireCompleteResponsesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"get", "delete"} {
		for _, state := range []string{"complete", "whitespace", "short content length", "interrupted chunks", "extra JSON", "trailing garbage", "no content"} {
			if operation == "get" && state == "no content" {
				continue // This client explicitly permits 204 without decoding.
			}
			t.Run(operation+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				tokenPath := filepath.Join(root, "token.json")
				if err := os.WriteFile(tokenPath, []byte(`{"access_token":"test-cella"}`), 0600); err != nil {
					t.Fatal(err)
				}
				payload := `{"id":"test-id","name":"dev"}`
				wantError := ""
				switch state {
				case "whitespace":
					payload += " \r\n\t"
				case "extra JSON":
					payload += ` {"id":"other"}`
					if operation == "get" {
						wantError = "multiple JSON values"
					}
				case "trailing garbage":
					payload += " garbage"
					if operation == "get" {
						wantError = "invalid character"
					}
				case "short content length", "interrupted chunks":
					wantError = "unexpected EOF"
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					method := http.MethodGet
					if operation == "delete" {
						method = http.MethodDelete
					}
					if r.Method != method || r.URL.Path != "/v1/sandboxes/dev" || r.Header.Get("Authorization") != "Bearer test-cella" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					if state == "no content" {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if state == "short content length" {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					_, _ = w.Write([]byte(payload))
					if state == "interrupted chunks" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "cella", operation, "dev", "--api-url", server.URL)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if wantError != "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), wantError) {
						t.Errorf("invalid response = %v; stderr: %q, want %q", err, stderr.String(), wantError)
					}
					if stdout.Len() != 0 || strings.Contains(stderr.String(), "deleted dev") {
						t.Errorf("incomplete response reported success: stdout=%q stderr=%q", stdout.String(), stderr.String())
					}
				} else if err != nil {
					t.Errorf("complete response failed: %v; %s", err, stderr.String())
				} else if operation == "get" && !strings.Contains(stdout.String(), "test-id") || operation == "delete" && !strings.Contains(stderr.String(), "deleted dev") {
					t.Errorf("missing successful result: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				if calls.Load() != 1 {
					t.Errorf("requests = %d, want one; failed responses must not replay mutations", calls.Load())
				}
			})
		}
	}
}
