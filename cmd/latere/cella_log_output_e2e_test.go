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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCellaLogOutputFailureE2E(t *testing.T) {
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
	for _, args := range [][]string{
		{"logs", "dev", "cmd"}, {"logs", "dev", "cmd", "--follow"},
		{"run", "logs", "run"}, {"run", "logs", "run", "--follow"},
		{"exec", "dev", "echo"}, {"run", "dev", "echo", "--follow"},
	} {
		for _, fail := range []bool{false, true} {
			mode := "/writable"
			if fail {
				mode = "/read-only"
			}
			t.Run(strings.Join(args, " ")+mode, func(t *testing.T) {
				var polls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/dev/commands" {
						_, _ = w.Write([]byte(`{"command_id":"cmd"}`))
						return
					}
					if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/logs") {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					polls.Add(1)
					_, _ = w.Write([]byte(`{"bytes":"remote output\n","next_cursor":14,"phase":"exited","exit_code":0}`))
				}))
				defer server.Close()
				outputPath := filepath.Join(t.TempDir(), "output")
				if err := os.WriteFile(outputPath, nil, 0600); err != nil {
					t.Fatal(err)
				}
				flags := os.O_WRONLY
				if fail {
					flags = os.O_RDONLY
				}
				file, err := os.OpenFile(outputPath, flags, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				argv := append([]string{"cella"}, args...)
				argv = append(argv, "--api-url", server.URL)
				command := exec.CommandContext(ctx, binary, argv...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = file, &diagnostic
				err = command.Run()
				want := "remote output\n"
				if fail {
					want = ""
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write logs") {
						t.Errorf("failed output returned %v: %s", err, diagnostic.String())
					}
				} else if err != nil {
					t.Errorf("valid output returned %v: %s", err, diagnostic.String())
				}
				if polls.Load() != 1 {
					t.Errorf("log polls = %d, want 1", polls.Load())
				}
				if got, err := os.ReadFile(outputPath); err != nil || string(got) != want {
					t.Errorf("output = %q, %v; want %q", got, err, want)
				}
			})
		}
	}
}
