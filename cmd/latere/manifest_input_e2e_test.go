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
	for _, product := range []string{"environments"} {
		limit := 64 << 10
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
					// An Environments create reads the account's labels
					// from the platform first, here on the same server.
					if r.URL.Path == "/v1/account/environments/context" {
						_, _ = io.WriteString(w, `{"tenant":{"labels":{"platform.latere.ai/tenant":"p-1","platform.latere.ai/principal":"p1"}}}`)
						return
					}
					requests.Add(1)
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/yaml" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					data, err := io.ReadAll(r.Body)
					want := manifest + "metadata:\n  labels:\n    platform.latere.ai/principal: \"p1\"\n    platform.latere.ai/tenant: \"p-1\"\n"
					if err != nil || string(data) != want {
						t.Errorf("wrong manifest applied: %q %v", data, err)
					}
					if r.URL.Path != "/v1/environments/sandboxes" {
						t.Errorf("path=%s", r.URL.Path)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"kind":"Sandbox","metadata":{"name":"intended"},"status":{"id":"sbx-1","phase":"Running"}}`)
				}))
				defer server.Close()
				// The existing helper configures the full command tree's inherited input.
				args := []string{"-test.run=^TestConfiguredInputHelperProcess$", "--", product, "apply", "-f", "-", "--api-url", server.URL + "/v1/environments"}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader("suite: wrong\n")
				command.Env = append(os.Environ(), "LATERE_ENVIRONMENTS_TOKEN=synthetic-token", "LATERE_PLATFORM_URL="+server.URL, "LATERE_PLATFORM_TOKEN=synthetic-token", "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_CONFIGURED_INPUT="+source)
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
