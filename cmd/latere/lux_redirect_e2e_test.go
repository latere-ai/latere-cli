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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLuxInvokeRedirectsPreservePromptE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	const prompt = "Keep this prompt intact"
	const reply = `{"choices":[{"message":{"content":"complete answer"}}]}`
	for _, raw := range []bool{false, true} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			mode := "text"
			if raw {
				mode = "json"
			}
			t.Run(mode+"/"+strconv.Itoa(status), func(t *testing.T) {
				root := t.TempDir()
				invalid := status < 307
				var initial, redirected atomic.Int32
				var original atomic.Value
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/lux/v1/providers" {
						_, _ = w.Write([]byte(`{"items":[]}`))
						return
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if r.URL.Path == "/redirected" {
						redirected.Add(1)
						if !invalid && (r.Method != http.MethodPost || string(body) != original.Load() || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-lux") {
							t.Error("redirect changed inference method, body or headers")
						}
						_, _ = w.Write([]byte(reply))
						return
					}
					initial.Add(1)
					if r.URL.Path != "/openai/v1/chat/completions" || r.Method != http.MethodPost || !bytes.Contains(body, []byte(prompt)) {
						t.Errorf("unexpected inference request: %s %s %q", r.Method, r.URL.Path, body)
					}
					original.Store(string(body))
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(status)
				}))
				defer server.Close()
				args := []string{"lux", "invoke", "--lux-url", server.URL, "--token", "test-lux", "--provider", "openai", "--model", "test-model", prompt}
				if raw {
					args = append(args, "--json")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				wantRedirected := int32(1)
				if invalid {
					wantRedirected = 0
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "redirect changed request method") {
						t.Errorf("method-changing redirect=%v, stderr=%q", err, stderr.String())
					}
					if stdout.Len() != 0 {
						t.Errorf("discarded prompt produced an answer: %q", stdout.String())
					}
				} else {
					want := "complete answer\n"
					if raw {
						want = reply + "\n"
					}
					if err != nil || stdout.String() != want {
						t.Errorf("valid redirect=%v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
				}
				if initial.Load() != 1 || redirected.Load() != wantRedirected {
					t.Errorf("initial/redirected calls=%d/%d, want 1/%d", initial.Load(), redirected.Load(), wantRedirected)
				}
			})
		}
	}
}
