// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
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

func TestApplyConfiguredManifestInputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	token := filepath.Join(dir, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, product := range []string{"cella", "sandbox", "eval"} {
		limit := 64 << 10
		if product == "eval" {
			limit = 256 << 10
		}
		for _, mode := range []string{"valid", "empty", "oversized"} {
			t.Run(product+"/"+mode, func(t *testing.T) {
				const manifest = "suite: intended\n"
				body, wantError := manifest, ""
				switch mode {
				case "empty":
					body, wantError = " \n", "manifest is empty"
				case "oversized":
					body, wantError = manifest+strings.Repeat("#", limit), "byte limit"
				}
				source := filepath.Join(t.TempDir(), "source.yaml")
				if err := os.WriteFile(source, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/yaml" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					data, err := io.ReadAll(r.Body)
					if err != nil || string(data) != manifest {
						t.Errorf("wrong manifest applied: %q %v", data, err)
					}
					if product == "eval" {
						if r.URL.Path != "/api/v1/suites/apply" {
							t.Errorf("path=%s", r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"dry_run":false,"suite":{"id":"st-1","name":"intended","status":"exists"}}`)
					} else {
						if r.URL.Path != "/v1/sandboxes" {
							t.Errorf("path=%s", r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"id":"sb-1","name":"intended"}`)
					}
				}))
				defer server.Close()
				// The existing helper configures the full command tree's inherited input.
				args := []string{"-test.run=^TestCellaConfiguredInputHelperProcess$", "--", product, "apply", "-f", "-", "--api-url", server.URL}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader("suite: wrong\n")
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "EVAL_ADMIN_TOKEN=synthetic-token", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_CONFIGURED_INPUT="+source)
				out, err := command.CombinedOutput()
				if wantError == "" {
					if err != nil || requests.Load() != 1 {
						t.Errorf("apply: %v requests=%d output=%q", err, requests.Load(), out)
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), wantError) || requests.Load() != 0 {
					t.Errorf("error=%v output=%q requests=%d, want %q before apply", err, out, requests.Load(), wantError)
				}
			})
		}
	}
}
