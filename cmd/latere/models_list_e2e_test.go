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

func TestModelsListOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const list = `{"object":"list","data":[{"id":"anthropic/claude-sonnet-4.6","object":"model","owned_by":"lux"},{"id":"openai/gpt-4.1-mini","object":"model","owned_by":"lux"}]}`
	for _, empty := range []bool{false, true} {
		for _, writable := range []bool{true, false} {
			t.Run(fmt.Sprintf("empty=%t/writable=%t", empty, writable), func(t *testing.T) {
				root := t.TempDir()
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.URL.Path != "/v1/models/openai/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					if empty {
						_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
						return
					}
					_, _ = io.WriteString(w, list)
				}))
				defer server.Close()
				output := filepath.Join(root, "output")
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
				defer func() { _ = file.Close() }()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "models", "--models-url", server.URL+"/v1/models")
				command.Env = modelsEnv(root, "synthetic-key")
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = file, &diagnostic
				err = command.Run()
				want := "previous\n"
				if writable {
					if empty {
						want += "No models.\n"
					} else {
						want += "anthropic/claude-sonnet-4.6\nopenai/gpt-4.1-mini\n"
					}
					if err != nil {
						t.Errorf("output failed: %v %q", err, diagnostic.String())
					}
					if !empty && !strings.Contains(diagnostic.String(), "console's Models section") {
						t.Errorf("stderr %q does not say where prices are", diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write model list") {
					t.Errorf("ignored output failure: %v %q", err, diagnostic.String())
				}
				data, readErr := os.ReadFile(output)
				if readErr != nil || string(data) != want {
					t.Errorf("output=%q read=%v, want %q", data, readErr, want)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}

// TestLuxIsAnUnknownCommandE2E: the retired namespace answers as any
// unknown command does, with no alias behind it.
func TestLuxIsAnUnknownCommandE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	root := t.TempDir()
	for _, args := range [][]string{{"lux"}, {"lux", "models"}, {"lux", "env", "--raw"}} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = modelsEnv(root, "")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		cancel()
		if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), `unknown command "lux"`) || stdout.Len() != 0 {
			t.Errorf("%v = %v; stdout %q stderr %q", args, err, stdout.String(), stderr.String())
		}
	}
}
