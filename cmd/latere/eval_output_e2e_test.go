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

func TestEvalOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	manifest := filepath.Join(root, "suite.yaml")
	if err := os.WriteFile(manifest, []byte("suite: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		args           []string
		response, want string
	}{
		{"apply", []string{"apply", "-f", manifest}, `{"dry_run":false,"suite":{"id":"st-1","name":"test","status":"created"}}`, "suite test (created)"},
		{"dry-run", []string{"apply", "-f", manifest, "--dry-run"}, `{"dry_run":true,"suite":{"id":"st-1","name":"test","status":"created"}}`, "dry run — no changes written"},
		{"empty suites", []string{"suites"}, `[]`, "No suites."},
		{"empty cells", []string{"cells", "--suite", "st-1"}, `[]`, "No cells in this suite."},
		{"suites", []string{"suites"}, `[{"id":"st-1","name":"test"}]`, "st-1"},
		{"cells", []string{"cells", "--suite", "st-1"}, `[{"tuple":{"model_id":"test-model"}}]`, "test-model"},
	} {
		for _, writable := range []bool{true, false} {
			mode := "read-only"
			if writable {
				mode = "writable"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Error("missing synthetic auth")
					}
					_, _ = io.WriteString(w, tc.response)
				}))
				defer server.Close()
				output := filepath.Join(t.TempDir(), "output")
				const previous = "existing output\n"
				if err := os.WriteFile(output, []byte(previous), 0600); err != nil {
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
				args := append([]string{"eval"}, tc.args...)
				args = append(args, "--api-url", server.URL)
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "EVAL_ADMIN_TOKEN=synthetic-token", "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = file, &diagnostic
				err = command.Run()
				data, readErr := os.ReadFile(output)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if writable {
					if err != nil || diagnostic.Len() != 0 || !strings.HasPrefix(string(data), previous) || !strings.Contains(string(data), tc.want) {
						t.Errorf("successful output: err=%v output=%q stderr=%q", err, data, diagnostic.String())
					}
				} else {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || diagnostic.Len() == 0 {
						t.Errorf("failed output: err=%v stderr=%q", err, diagnostic.String())
					}
					if string(data) != previous {
						t.Errorf("failed output altered file: %q", data)
					}
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
