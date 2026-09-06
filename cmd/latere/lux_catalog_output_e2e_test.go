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

func TestLuxCatalogOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	const item = `{"model":"test-model","provider":"test","status":"active"}`
	const rate = `{"model":"test-model","provider":"test","input_usd_per_m":2,"output_usd_per_m":3}`
	const record = "model:      test-model\nprovider:   test\nstatus:     active\n"
	for _, tc := range []struct {
		name, body, record, empty string
		requests                  int32
	}{
		{"models", item, record + "rate:       $2/M in, $3/M out\n", "No models.\n", 2},
		{"providers", item, record, "No providers Lux can route to.\n", 1},
		{"rates", rate, "model:      test-model\nprovider:   test\ninput_usd_per_m:2\noutput_usd_per_m:3\n", "No model rate card.\n", 1},
	} {
		for _, empty := range []bool{false, true} {
			for _, writable := range []bool{true, false} {
				name := fmt.Sprintf("%s/empty=%t/read-only", tc.name, empty)
				if writable {
					name = fmt.Sprintf("%s/empty=%t/writable", tc.name, empty)
				}
				t.Run(name, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Errorf("request=%s %s", r.Method, r.URL)
						}
						switch r.URL.Path {
						case "/lux/v1/" + tc.name:
							if empty {
								_, _ = io.WriteString(w, `{"items":[]}`)
							} else {
								_, _ = io.WriteString(w, `{"items":[`+tc.body+`,`+tc.body+`]}`)
							}
						case "/lux/v1/rates":
							_, _ = io.WriteString(w, `{"items":[`+rate+`]}`)
						default:
							t.Errorf("unexpected path=%s", r.URL.Path)
							w.WriteHeader(404)
						}
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
					command := exec.CommandContext(ctx, binary, "lux", tc.name, "--lux-url", server.URL, "--token", "synthetic-token")
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var diagnostic bytes.Buffer
					command.Stdout, command.Stderr = file, &diagnostic
					err = command.Run()
					want := "previous\n"
					if writable {
						if empty {
							want += tc.empty
						} else {
							want += tc.record + "\n" + tc.record
						}
						if err != nil || diagnostic.Len() != 0 {
							t.Errorf("output failed: %v %q", err, diagnostic.String())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write Lux") {
						t.Errorf("ignored output failure: %v %q", err, diagnostic.String())
					}
					data, readErr := os.ReadFile(output)
					if readErr != nil || string(data) != want {
						t.Errorf("output=%q read=%v, want %q", data, readErr, want)
					}
					if requests.Load() != tc.requests {
						t.Errorf("requests=%d, want %d", requests.Load(), tc.requests)
					}
				})
			}
		}
	}
}
