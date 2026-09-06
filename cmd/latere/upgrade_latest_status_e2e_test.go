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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/commands"
)

func TestUpgradeLatestStatusE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{302, 200, 304, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
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
				w.Header().Set("Location", "/latere-ai/latere-cli/releases/tag/v9.9.9")
				w.WriteHeader(status)
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
			if status == 302 {
				if runErr != nil || diagnostic.Len() != 0 || !strings.Contains(out.String(), "v1.0.0 -> v9.9.9") || !bytes.Contains(cached, []byte("v9.9.9")) {
					t.Errorf("valid redirect: error=%v stdout=%q stderr=%q cache=%q", runErr, out.String(), diagnostic.String(), cached)
				}
			} else if exit, ok := errors.AsType[*exec.ExitError](runErr); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), fmt.Sprintf("status %d", status)) || !bytes.Equal(cached, before) {
				t.Errorf("invalid status: error=%v stdout=%q stderr=%q cache=%q", runErr, out.String(), diagnostic.String(), cached)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}

func TestUpgradeLatestStatusHelperProcess(t *testing.T) {
	fixture := os.Getenv("LATERE_TEST_LATEST_SERVER")
	if fixture == "" {
		return
	}
	target, err := url.Parse(fixture)
	if err != nil || target.Hostname() != "127.0.0.1" {
		t.Fatal("expected loopback fixture")
	}
	http.DefaultTransport = upgradeFixtureTransport{target: target, base: &http.Transport{Proxy: nil}}
	root := commands.NewRoot("v1.0.0")
	args := []string{"upgrade", "--check"}
	if target, present := os.LookupEnv("LATERE_TEST_LATEST_TARGET"); present {
		args = append(args, target)
	}
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
	os.Exit(0)
}
