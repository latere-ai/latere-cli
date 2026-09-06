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

func TestCellaRunIDOutputFailureE2E(t *testing.T) {
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
	for _, detached := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			name, id := "background", "cmd-123"
			if detached {
				name, id = "detached", "run-123"
			}
			if fail {
				name += "/read-only stdout"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					path := "/v1/sandboxes/dev/commands"
					if detached {
						path = "/v1/one-shot-runs"
						if r.URL.Query().Get("detach") != "true" {
							t.Error("one-shot run was not detached")
						}
					}
					if r.Method != http.MethodPost || r.URL.Path != path {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"command_id":"cmd-123","run_id":"run-123","state":"creating","phase":"running"}`))
				}))
				defer server.Close()
				args := []string{"cella", "run", "--api-url", server.URL}
				if detached {
					args = append(args, "--ephemeral", "--rm", "--detach")
				} else {
					args = append(args, "dev")
				}
				args = append(args, "--", "echo")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if fail {
					path := filepath.Join(t.TempDir(), "read-only")
					if err := os.WriteFile(path, nil, 0600); err != nil {
						t.Fatal(err)
					}
					file, err := os.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					command.Stdout = file
				}
				err := command.Run()
				if fail {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("failed output returned %v: %s", err, stderr.String())
					}
					if !strings.Contains(stderr.String(), id) || !strings.Contains(stderr.String(), "started") || !strings.Contains(stderr.String(), "writing its ID failed") {
						t.Errorf("missing recovery diagnostic: %q", stderr.String())
					}
				} else if err != nil || stdout.String() != id+"\n" {
					t.Errorf("ID output = %q, %v: %s", stdout.String(), err, stderr.String())
				}
				if requests.Load() != 1 {
					t.Errorf("start requests = %d, want 1", requests.Load())
				}
			})
		}
	}
}
