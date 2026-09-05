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

func TestLuxInvokeReportsOutputFailuresE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, provider := range []string{"openai", "anthropic"} {
		for _, format := range []string{"text", "json"} {
			for _, mode := range []string{"writable", "read only"} {
				t.Run(provider+"/"+format+"/"+mode, func(t *testing.T) {
					root := t.TempDir()
					dest := filepath.Join(root, "answer")
					const before = "existing answer\n"
					if err := os.WriteFile(dest, []byte(before), 0600); err != nil {
						t.Fatal(err)
					}
					flags := os.O_WRONLY | os.O_APPEND
					if mode == "read only" {
						flags = os.O_RDONLY
					}
					file, err := os.OpenFile(dest, flags, 0600)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = file.Close() }()
					response, path := `{"choices":[{"message":{"content":"test answer"}}]}`, "/openai/v1/chat/completions"
					if provider == "anthropic" {
						response, path = `{"content":[{"type":"text","text":"test answer"}]}`, "/anthropic/v1/messages"
					}
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/lux/v1/providers" {
							_, _ = w.Write([]byte(`{"items":[]}`))
							return
						}
						calls.Add(1)
						body, err := io.ReadAll(r.Body)
						if err != nil || r.URL.Path != path || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-lux" || !bytes.Contains(body, []byte("test prompt")) {
							t.Errorf("unexpected inference request: %s %s (%v)", r.Method, r.URL.Path, err)
						}
						_, _ = w.Write([]byte(response))
					}))
					defer server.Close()
					args := []string{"lux", "invoke", "--lux-url", server.URL, "--token", "test-lux", "--provider", provider, "--model", "test-model", "test prompt"}
					if format == "json" {
						args = append(args, "--json")
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
					var stderr bytes.Buffer
					command.Stdout, command.Stderr = file, &stderr
					err = command.Run()
					want := before
					if mode == "read only" {
						if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "write") {
							t.Errorf("lost answer reported success: %v: %s", err, stderr.String())
						}
					} else {
						if format == "json" {
							want += response + "\n"
						} else {
							want += "test answer\n"
						}
						if err != nil || stderr.Len() != 0 {
							t.Errorf("writable output failed: %v: %s", err, stderr.String())
						}
					}
					if data, err := os.ReadFile(dest); err != nil || string(data) != want {
						t.Errorf("answer file=%q (%v), want %q", data, err, want)
					}
					if calls.Load() != 1 {
						t.Errorf("inference requests=%d, want 1", calls.Load())
					}
				})
			}
		}
	}
}
