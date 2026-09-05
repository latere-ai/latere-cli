// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestLuxEnvValidatesAndReportsLifetimeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, raw := range []bool{false, true} {
		mode := "exports"
		if raw {
			mode = "raw"
		}
		for _, tc := range []struct {
			name, ttl, override, wantError, wantNote string
			seconds                                  int
			omitLifetime                             bool
		}{
			{name: "default"},
			{name: "zero", ttl: "0s", wantError: "positive whole number of seconds"},
			{name: "negative", ttl: "-1m", wantError: "positive whole number of seconds"},
			{name: "subsecond", ttl: "500ms", wantError: "positive whole number of seconds"},
			{name: "fractional", ttl: "1500ms", wantError: "positive whole number of seconds"},
			{name: "one second", ttl: "1s", seconds: 1, wantNote: "expires in 1 second"},
			{name: "server capped", ttl: "1h", seconds: 3600, wantNote: "expires in 300 seconds"},
			{name: "unknown expiry", ttl: "1m", seconds: 60, omitLifetime: true, wantNote: "expiry not reported by auth"},
			{name: "flag override with ttl", ttl: "1m", override: "flag", wantError: "--ttl cannot be combined"},
			{name: "env override with ttl", ttl: "1m", override: "env", wantError: "--ttl cannot be combined"},
			{name: "flag override without ttl", override: "flag"},
			{name: "env override without ttl", override: "env"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "auth-token.json")
				authBefore := []byte(`{"access_token":"saved-root"}`)
				if err := os.WriteFile(authPath, authBefore, 0600); err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body struct {
						TTL int `json:"ttl_seconds"`
					}
					if r.URL.Path != "/actor-tokens" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer saved-root" || json.NewDecoder(r.Body).Decode(&body) != nil || body.TTL != tc.seconds {
						t.Errorf("invalid mint request: %s %s, ttl=%d want %d", r.Method, r.URL.Path, body.TTL, tc.seconds)
					}
					response := map[string]any{"actor_token": "short-actor"}
					if !tc.omitLifetime {
						response["expires_in"] = min(max(body.TTL, 1), 300)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer server.Close()
				args := []string{"lux", "env", "--auth-url", server.URL, "--lux-url", server.URL}
				if raw {
					args = append(args, "--raw")
				} else {
					args = append(args, "--compat", "openai")
				}
				if tc.ttl != "" {
					args = append(args, "--ttl", tc.ttl)
				}
				if tc.override == "flag" {
					args = append(args, "--token", "provided-token")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				envToken := ""
				if tc.override == "env" {
					envToken = "provided-token"
				}
				command.Env = append(os.Environ(), "LATERE_LUX_TOKEN="+envToken, "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				var wantCalls int32
				if tc.wantError != "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), tc.wantError) {
						t.Errorf("invalid lifetime=%v; stderr=%q, want %q", err, stderr.String(), tc.wantError)
					}
					if stdout.Len() != 0 {
						t.Errorf("invalid lifetime exported a credential: %q", stdout.String())
					}
				} else {
					wantToken := "saved-root"
					if tc.override != "" {
						wantToken = "provided-token"
					}
					if tc.seconds > 0 {
						wantCalls = 1
						wantToken = "short-actor"
					}
					if err != nil || !strings.Contains(stdout.String(), wantToken) || !strings.Contains(stderr.String(), tc.wantNote) {
						t.Errorf("valid lifetime=%v; stdout=%q stderr=%q, want note %q", err, stdout.String(), stderr.String(), tc.wantNote)
					}
				}
				if calls.Load() != wantCalls {
					t.Errorf("mint calls=%d, want %d", calls.Load(), wantCalls)
				}
				if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, authBefore) {
					t.Errorf("saved auth credential changed: %v", err)
				}
			})
		}
	}
}
