// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestPolicyOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	token := filepath.Join(dir, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, verb := range [][]string{{"policy"}, {"policy", "list"}, {"policies"}} {
			for _, empty := range []bool{false, true} {
				for _, writable := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%v/empty=%t/writable=%t", prefix, verb, empty, writable), func(t *testing.T) {
						var requests atomic.Int32
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests.Add(1)
							if r.Method != http.MethodGet || r.URL.Path != "/v1/policies" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
							data := `[{"name":"restricted","selectable":true}]`
							if empty {
								data = "[]"
							}
							_, _ = io.WriteString(w, data)
						}))
						defer server.Close()
						output := filepath.Join(t.TempDir(), "output")
						if err := os.WriteFile(output, []byte("previous\n"), 0600); err != nil {
							t.Fatal(err)
						}
						flags := os.O_RDONLY
						if writable {
							flags = os.O_WRONLY | os.O_APPEND
						}
						file, err := os.OpenFile(output, flags, 0600)
						if err != nil {
							t.Fatal(err)
						}
						defer file.Close()
						ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
						defer cancel()
						args := append([]string{prefix}, verb...)
						command := exec.CommandContext(ctx, binary, append(args, "--api-url", server.URL)...)
						command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
						var diagnostic bytes.Buffer
						command.Stdout, command.Stderr = file, &diagnostic
						err = command.Run()
						data, readErr := os.ReadFile(output)
						if readErr != nil {
							t.Fatal(readErr)
						}
						if writable {
							if err != nil || diagnostic.Len() != 0 || !strings.HasPrefix(string(data), "previous\n") {
								t.Errorf("output=%q error=%v stderr=%q", data, err, diagnostic.String())
							}
							expected := "policy:     restricted\n"
							if empty {
								expected = "No policy profiles are visible to this token.\n"
							}
							if !strings.Contains(string(data), expected) {
								t.Errorf("missing policy output: %q", data)
							}
						} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write policy") || string(data) != "previous\n" {
							t.Errorf("ignored output failure: %v stderr=%q output=%q", err, diagnostic.String(), data)
						}
						if requests.Load() != 1 {
							t.Errorf("requests=%d, want 1", requests.Load())
						}
					})
				}
			}
		}
	}
}
