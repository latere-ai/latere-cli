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

func TestCellaLsOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	token := filepath.Join(root, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, mode := range []string{"writable", "read-only"} {
			t.Run(prefix+"/"+mode, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes/dev/files" || r.URL.Query().Get("path") != "/workspace" || r.URL.Query().Get("list") != "true" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, `{"entries":[{"name":"a.txt","size":3,"mode":420},{"name":"dir","size":0,"mode":493,"is_directory":true}]}`)
				}))
				defer server.Close()
				output := filepath.Join(t.TempDir(), "output")
				const previous = "existing output\n"
				if err := os.WriteFile(output, []byte(previous), 0600); err != nil {
					t.Fatal(err)
				}
				flags := os.O_RDONLY
				if mode == "writable" {
					flags = os.O_WRONLY | os.O_APPEND
				}
				file, err := os.OpenFile(output, flags, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, prefix, "ls", "dev", "/workspace", "--api-url", server.URL)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = file, &diagnostic
				err = command.Run()
				want := previous
				if mode == "writable" {
					want += "0644\t3\ta.txt\n0755\t0\tdir/\n"
					if err != nil || diagnostic.Len() != 0 {
						t.Errorf("valid output failed: %v: %s", err, diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write directory listing") {
					t.Errorf("failed output returned %v: %s", err, diagnostic.String())
				}
				if data, err := os.ReadFile(output); err != nil || string(data) != want {
					t.Errorf("output=%q, error=%v; want %q", data, err, want)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
