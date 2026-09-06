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

func TestEvalApplyResponseValidationE2E(t *testing.T) {
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
	for _, dry := range []bool{false, true} {
		valid := fmt.Sprintf(`{"dry_run":%t,"suite":{"id":"st-1","name":"test","status":"created"}}`, dry)
		for _, tc := range []struct {
			name, body string
			valid      bool
		}{
			{"valid", valid, true},
			{"future status", strings.Replace(valid, `"created"`, `"reconciled","extra":true`, 1), true},
			{"null", `null`, false},
			{"empty", `{}`, false},
			{"missing id", strings.Replace(valid, `"id":"st-1",`, "", 1), false},
			{"missing name", strings.Replace(valid, `"name":"test",`, "", 1), false},
			{"missing status", strings.Replace(valid, `,"status":"created"`, "", 1), false},
			{"missing mode", strings.Replace(valid, fmt.Sprintf(`"dry_run":%t,`, dry), "", 1), false},
			{"null mode", strings.Replace(valid, fmt.Sprintf(`"dry_run":%t`, dry), `"dry_run":null`, 1), false},
			{"wrong mode", strings.Replace(valid, fmt.Sprintf(`"dry_run":%t`, dry), fmt.Sprintf(`"dry_run":%t`, !dry), 1), false},
		} {
			t.Run(fmt.Sprintf("dry=%t/%s", dry, tc.name), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Error("missing synthetic auth")
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				args := []string{"eval", "apply", "-f", manifest, "--api-url", server.URL}
				if dry {
					args = append(args, "--dry-run")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "EVAL_ADMIN_TOKEN=synthetic-token", "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				if tc.valid {
					if err != nil || diagnostic.Len() != 0 || !strings.Contains(out.String(), "suite test") || strings.Contains(out.String(), "dry run") != dry {
						t.Errorf("valid response: err=%v output=%q stderr=%q", err, out.String(), diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "invalid Eval apply response") || !strings.Contains(diagnostic.String(), "outcome is unknown") || out.Len() != 0 {
					t.Errorf("invalid response: err=%v output=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
