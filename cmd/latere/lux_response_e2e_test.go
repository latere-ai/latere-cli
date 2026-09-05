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
	"testing"
	"time"
)

func TestLuxInvokeRejectsIncompleteResponsesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	const reply = `{"choices":[{"message":{"content":"complete answer"}}]}`
	for _, raw := range []bool{false, true} {
		mode := "text"
		if raw {
			mode = "json"
		}
		for _, tc := range []struct {
			name, wantError string
			size            int
		}{
			{name: "complete"},
			{name: "short content length", wantError: "unexpected EOF"},
			{name: "interrupted chunks", wantError: "unexpected EOF"},
			{name: "at limit", size: 8 << 20},
			{name: "over limit", size: (8 << 20) + 1, wantError: "exceeds 8 MiB"},
			{name: "provider error", wantError: "provider_busy"},
			{name: "provider error over limit", size: (8 << 20) + 1, wantError: "provider_busy"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				payload := reply
				if strings.HasPrefix(tc.name, "provider error") {
					payload = `{"error":{"code":"provider_busy","message":"try again later"}}`
				}
				if tc.size > len(payload) {
					// JSON permits trailing whitespace. Truncating it previously
					// hid the size violation even in parsed-text mode.
					payload += strings.Repeat(" ", tc.size-len(payload))
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/lux/v1/providers" {
						_, _ = w.Write([]byte(`{"items":[]}`))
						return
					}
					if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-lux-token" {
						t.Errorf("unexpected inference request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if strings.HasPrefix(tc.name, "provider error") {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(payload))
						return
					}
					if tc.name == "short content length" {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					_, _ = w.Write([]byte(payload))
					if tc.name == "interrupted chunks" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				args := []string{"lux", "invoke", "--lux-url", server.URL, "--token", "test-lux-token", "--provider", "openai", "--model", "test-model", "Say hello"}
				if raw {
					args = append(args, "--json")
				}
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if tc.wantError != "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("invalid response exit = %v; stderr: %s", err, stderr.String())
					}
					if !strings.Contains(stderr.String(), tc.wantError) {
						t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantError)
					}
					if stdout.Len() != 0 {
						t.Errorf("invalid response printed %d bytes to stdout", stdout.Len())
					}
					return
				}
				want := "complete answer\n"
				if raw {
					want = reply + "\n"
				}
				if err != nil || stdout.String() != want {
					t.Errorf("complete response = %v, stdout bytes=%d; stderr: %s", err, stdout.Len(), stderr.String())
				}
			})
		}
	}
}
