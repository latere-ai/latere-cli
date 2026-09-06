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

func TestLuxUsageOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"empty", "chart"} {
		for _, writable := range []bool{true, false} {
			name := mode + "/read-only"
			if writable {
				name = mode + "/writable"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					body := `{"items":[]}`
					switch r.URL.Path {
					case "/lux/v1/usage":
						if mode == "chart" {
							body = `{"items":[{"group":"alpha","calls":1,"tokens_in":20,"tokens_out":10,"cost_usd_micro":2000000}]}`
						}
					case "/lux/v1/usage/series":
						if mode == "chart" {
							body = `{"items":[{"ts":"2026-07-10T00:00:00Z","cost_usd_micro":2000000}]}`
						}
					default:
						t.Errorf("unexpected path=%s", r.URL.Path)
					}
					_, _ = io.WriteString(w, body)
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
				command := exec.CommandContext(ctx, binary, "lux", "usage", "--lux-url", server.URL, "--token", "synthetic-token")
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = file, &diagnostic
				err = command.Run()
				data, readErr := os.ReadFile(output)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if writable {
					if err != nil || diagnostic.Len() != 0 || !strings.HasPrefix(string(data), "previous\nUsage last month") {
						t.Errorf("output=%q error=%v stderr=%q", data, err, diagnostic.String())
					}
					if mode == "empty" {
						if !strings.HasSuffix(string(data), "No usage in this period.\n") {
							t.Errorf("empty usage=%q", data)
						}
					} else if !strings.Contains(string(data), "alpha") || !strings.Contains(string(data), "▇") {
						t.Errorf("missing table/chart: %q", data)
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write Lux usage") || string(data) != "previous\n" {
					t.Errorf("ignored output failure: %v stderr=%q output=%q", err, diagnostic.String(), data)
				}
				if requests.Load() != 2 {
					t.Errorf("requests=%d, want 2", requests.Load())
				}
			})
		}
	}
}
