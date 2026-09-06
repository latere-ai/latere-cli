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

func TestLuxInvokeConfiguredOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, tc := range []struct{ provider, path, body string }{
		{"openai", "/openai/v1/chat/completions", `{"choices":[{"message":{"content":"first\nsecond"}}]}`},
		{"anthropic", "/anthropic/v1/messages", `{"content":[{"type":"text","text":"first\nsecond"}]}`},
	} {
		for _, format := range []string{"text", "json"} {
			for _, writable := range []string{"1", "0"} {
				t.Run(tc.provider+"/"+format+"/writable="+writable, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Method != http.MethodPost || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Errorf("request=%s %s", r.Method, r.URL)
						}
						_, _ = io.WriteString(w, " \n"+tc.body+"\n ")
					}))
					defer server.Close()
					output := filepath.Join(t.TempDir(), "output.json")
					if err := os.WriteFile(output, nil, 0600); err != nil {
						t.Fatal(err)
					}
					// Reuse the helper that installs an inherited writer on the full command tree.
					args := []string{"-test.run=^TestCellaDownloadOutputHelperProcess$", "--", "lux", "invoke", "test", "--model", "test-model", "--provider", tc.provider, "--lux-url", server.URL, "--token", "synthetic-token"}
					if format == "json" {
						args = append(args, "--json")
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_DOWNLOAD_OUTPUT="+output, "LATERE_TEST_DOWNLOAD_WRITABLE="+writable)
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					data, readErr := os.ReadFile(output)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if writable == "1" {
						want := "first\nsecond\n"
						if format == "json" {
							want = strings.TrimSpace(tc.body) + "\n"
						}
						if err != nil || diagnostic.Len() != 0 || string(data) != want {
							t.Errorf("output=%q error=%v stderr=%q, want %q", data, err, diagnostic.String(), want)
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || diagnostic.Len() == 0 || len(data) != 0 {
						t.Errorf("write failure: error=%v stderr=%q output=%q", err, diagnostic.String(), data)
					}
					if out.Len() != 0 {
						t.Errorf("result leaked to process stdout: %q", out.String())
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d, want %d", requests.Load(), 1)
					}
				})
			}
		}
	}
}
