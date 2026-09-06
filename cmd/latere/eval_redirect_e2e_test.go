// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestEvalApplyRedirectsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	manifest := filepath.Join(root, "suite.yaml")
	const body = "suite: test\n"
	if err := os.WriteFile(manifest, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, mode := range []string{"live", "dry-run retained", "dry-run equivalent", "live equivalent", "dry-run dropped"} {
			t.Run(fmt.Sprintf("%d/%s", status, mode), func(t *testing.T) {
				dryRun := mode != "live" && mode != "live equivalent"
				valid := status >= 307 && mode != "dry-run dropped"
				var initial, redirected, writes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if r.URL.Path == "/target" {
						redirected.Add(1)
						receivedDryRun := r.URL.Query().Get("dry_run") == "1" || r.URL.Query().Get("dry_run") == "true"
						if r.Method == http.MethodPost && !receivedDryRun {
							writes.Add(1)
						}
						if valid && (r.Method != http.MethodPost || string(data) != body || r.Header.Get("Authorization") != "Bearer synthetic-token" || receivedDryRun != dryRun) {
							t.Error("redirect altered apply request")
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"dry_run": receivedDryRun, "suite": map[string]string{"id": "st-1", "name": "test", "status": "created"}})
						return
					}
					initial.Add(1)
					if r.URL.Path != "/api/v1/suites/apply" || r.Method != http.MethodPost || string(data) != body || (r.URL.Query().Get("dry_run") == "1") != dryRun {
						t.Errorf("unexpected initial request: %s %s", r.Method, r.URL)
					}
					target := "/target"
					switch mode {
					case "dry-run retained":
						target += "?dry_run=1"
					case "dry-run equivalent":
						target += "?dry_run=true"
					case "live equivalent":
						target += "?dry_run=false"
					}
					w.Header().Set("Location", target)
					w.WriteHeader(status)
				}))
				defer server.Close()
				args := []string{"eval", "apply", "-f", manifest, "--api-url", server.URL}
				if dryRun {
					args = append(args, "--dry-run")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "EVAL_ADMIN_TOKEN=synthetic-token", "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				wantRedirected, wantWrites := int32(0), int32(0)
				if valid {
					wantRedirected = 1
					if !dryRun {
						wantWrites = 1
					}
					if err != nil || !strings.Contains(out.String(), "suite test") || strings.Contains(out.String(), "dry run") != dryRun {
						t.Errorf("valid redirect: %v output=%q stderr=%q", err, out.String(), diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "redirect changed") || out.Len() != 0 {
					t.Errorf("unsafe redirect: %v output=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if initial.Load() != 1 || redirected.Load() != wantRedirected || writes.Load() != wantWrites {
					t.Errorf("initial=%d redirected=%d writes=%d, want 1/%d/%d", initial.Load(), redirected.Load(), writes.Load(), wantRedirected, wantWrites)
				}
			})
		}
	}
}
