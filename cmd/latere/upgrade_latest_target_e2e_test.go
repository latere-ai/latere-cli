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

func TestUpgradeLatestTargetE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const release = "/latere-ai/latere-cli/releases/tag/"
	for _, tc := range []struct {
		name, location string
		valid          bool
	}{
		{"valid", release + "v9.9.9", true},
		{"query", release + "v9.9.9?source=latest", true},
		{"fragment", release + "v9.9.9#notes", true},
		{"invalid version", release + "not-a-version", false},
		{"wrong repo", "/other/repo/releases/tag/v9.9.9", false},
		{"query lookalike", "/login?return_to=" + release + "v9.9.9", false},
		{"nested tag", release + "nested/v9.9.9", false},
		{"external host", "https://example.invalid" + release + "v9.9.9", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stateDir := filepath.Join(dir, "latere")
			if err := os.MkdirAll(stateDir, 0700); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(stateDir, "update-check.json")
			before := []byte(`{"latest_version":"v1.1.0","checked_at":"2026-01-01T00:00:00Z"}`)
			if err := os.WriteFile(state, before, 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodHead || r.URL.Path != "/latere-ai/latere-cli/releases/latest" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				w.Header().Set("Location", tc.location)
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "-test.run=^TestUpgradeLatestStatusHelperProcess$")
			command.Env = append(os.Environ(), "LATERE_TEST_LATEST_SERVER="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			runErr := command.Run()
			cached, err := os.ReadFile(state)
			if err != nil {
				t.Fatal(err)
			}
			if tc.valid {
				if runErr != nil || diagnostic.Len() != 0 || !strings.Contains(out.String(), "v1.0.0 -> v9.9.9") || !bytes.Contains(cached, []byte("v9.9.9")) {
					t.Errorf("valid redirect: error=%v stdout=%q stderr=%q cache=%q", runErr, out.String(), diagnostic.String(), cached)
				}
			} else if exit, ok := errors.AsType[*exec.ExitError](runErr); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "unexpected latest-release redirect") || !bytes.Equal(cached, before) {
				t.Errorf("invalid redirect: error=%v stdout=%q stderr=%q cache=%q", runErr, out.String(), diagnostic.String(), cached)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}
