// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
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

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestLoginWithClosedStdinE2E(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		name := "detect stdin"
		if explicit {
			name = "explicit token"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			tokenPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
			before := map[string]string{tokenPath: `{"access_token":"old-cella"}`, authPath: `{"access_token":"old-auth"}`}
			for path, data := range before {
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !explicit || r.URL.Path != "/v1/sandboxes" || r.Header.Get("Authorization") != "Bearer candidate" {
					t.Error("unexpected login request")
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestLoginClosedStdinHelperProcess$", "--", "login", "--no-git", "--no-browser", "--api-url", server.URL}
			if explicit {
				args = append(args, "--token", "candidate")
			}
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+authPath,
				"AUTH_URL="+server.URL, "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_LOGIN_CLOSED_STDIN=1")
			out, err := command.CombinedOutput()
			if explicit {
				if err != nil || requests.Load() != 1 {
					t.Fatalf("explicit token login failed: %v (%d requests): %s", err, requests.Load(), out)
				}
				if token, err := api.LoadToken(tokenPath); err != nil || token.AccessToken != "candidate" {
					t.Errorf("explicit token was not saved: %v", err)
				}
				return
			}
			if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "inspect stdin") || strings.Contains(string(out), "panic:") {
				t.Errorf("closed stdin did not return a normal error: %v: %s", err, out)
			}
			if requests.Load() != 0 {
				t.Errorf("unavailable stdin made %d requests", requests.Load())
			}
			for path, data := range before {
				if got, err := os.ReadFile(path); err != nil || string(got) != data {
					t.Errorf("unavailable stdin changed credentials: %v", err)
				}
			}
		})
	}
}

// Close the handle after Go's startup normalization, then run the real CLI
// entrypoint so its error handling and exit status are exercised in isolation.
func TestLoginClosedStdinHelperProcess(t *testing.T) {
	if os.Getenv("LATERE_TEST_LOGIN_CLOSED_STDIN") != "1" {
		return
	}
	os.Args = append([]string{"latere"}, os.Args[3:]...)
	if err := os.Stdin.Close(); err != nil {
		t.Fatal(err)
	}
	main()
	os.Exit(0)
}
