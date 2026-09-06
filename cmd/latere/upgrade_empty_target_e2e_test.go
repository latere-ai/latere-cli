// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestUpgradeEmptyTargetE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"", " ", "\t"} {
		t.Run(fmt.Sprintf("target=%q", target), func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "latere", "update-check.json")
			if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
				t.Fatal(err)
			}
			before := []byte(`{"latest_version":"v1.1.0","checked_at":"2026-01-01T00:00:00Z"}`)
			if err := os.WriteFile(state, before, 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodHead || r.URL.Path != "/latere-ai/latere-cli/releases/latest" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Location", "/latere-ai/latere-cli/releases/tag/v9.9.9")
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "-test.run=^TestUpgradeLatestStatusHelperProcess$")
			command.Env = append(os.Environ(), "LATERE_TEST_LATEST_TARGET="+target, "LATERE_TEST_LATEST_SERVER="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			runErr := command.Run()
			if exit, ok := errors.AsType[*exec.ExitError](runErr); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "invalid version") || requests.Load() != 0 {
				t.Errorf("error=%v stdout=%q stderr=%q requests=%d", runErr, out.String(), diagnostic.String(), requests.Load())
			}
			after, err := os.ReadFile(state)
			if err != nil || !bytes.Equal(after, before) {
				t.Errorf("update cache changed: %q error=%v", after, err)
			}
		})
	}
}
