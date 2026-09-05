// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
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

func TestCommandsPreserveHTTPErrorStatusE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name               string
		status, bodyStatus int
		want               string
	}{
		{"rates", 503, 200, "status 503: try later"},
		{"invoke", 503, 200, "status 503: try later"},
		{"logout unavailable", 503, 404, "warning: could not revoke the cella token server-side"},
		{"logout unsupported", 404, 503, "note: server-side token revocation unavailable (404)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tokenPath := filepath.Join(root, "token.json")
			if err := os.WriteFile(tokenPath, []byte(`{"access_token":"test-cella"}`), 0600); err != nil {
				t.Fatal(err)
			}
			logout := strings.HasPrefix(tc.name, "logout")
			path, method := "/lux/v1/rates", http.MethodGet
			if tc.name == "invoke" {
				path, method = "/openai/v1/chat/completions", http.MethodPost
			} else if logout {
				path, method = "/v1/tokens/current", http.MethodDelete
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/lux/v1/providers" {
					_, _ = w.Write([]byte(`{"items":[]}`))
					return
				}
				calls.Add(1)
				if r.URL.Path != path || r.Method != method {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"status":%d,"message":"try later"}`, tc.bodyStatus)
			}))
			defer server.Close()
			args := []string{"lux", "rates", "--json", "--token", "test-lux", "--lux-url", server.URL}
			if tc.name == "invoke" {
				args = []string{"lux", "invoke", "--token", "test-lux", "--lux-url", server.URL, "--provider", "openai", "--model", "test-model", "Hello"}
			} else if logout {
				args = []string{"logout", "--api-url", server.URL, "--auth-url", server.URL}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if logout {
				if err != nil {
					t.Errorf("logout failed: %v; %s", err, stderr.String())
				}
				if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("logout retained local credential: %v", err)
				}
			} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
				t.Errorf("HTTP failure exit = %v; %s", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.want)
			}
			if stdout.Len() != 0 || calls.Load() != 1 {
				t.Errorf("stdout=%q, requests=%d; want empty stdout and one request", stdout.String(), calls.Load())
			}
		})
	}
}
