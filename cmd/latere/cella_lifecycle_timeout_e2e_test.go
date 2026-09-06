// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/latere-ai/latere-cli/internal/commands"
)

type oneShotLifecycleTransport func(*http.Request) (*http.Response, error)

func (f oneShotLifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the complete CLI command tree with a virtual clock and transport:
// real lifecycle delays would otherwise add many minutes to every test run.
func TestOneShotLifecycleTimeoutE2E(t *testing.T) {
	root := t.TempDir()
	token := filepath.Join(root, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_TOKEN_FILE", token)
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, tc := range []struct {
			name, timeout      string
			delay, callerLimit time.Duration
			wantTimeout        bool
			invalid            bool
		}{
			{name: "fast", delay: time.Second},
			{name: "slow default", delay: 16 * time.Minute},
			{name: "slow maximum lifecycle", delay: 32 * time.Minute},
			{name: "overflow", timeout: "9223372036854775807", delay: time.Minute, invalid: true},
			{name: "slow explicit default", timeout: "0", delay: 16 * time.Minute},
			{name: "slow short command", timeout: "1", delay: 6*time.Minute + time.Second},
			{name: "caller cancellation", delay: 16 * time.Minute, callerLimit: 30 * time.Second, wantTimeout: true},
			{name: "stalled server", delay: time.Hour, wantTimeout: true},
		} {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					requests := 0
					http.DefaultTransport = oneShotLifecycleTransport(func(r *http.Request) (*http.Response, error) {
						requests++
						if r.Method != http.MethodPost || r.URL.Path != "/v1/one-shot-runs" || r.Header.Get("Authorization") != "Bearer test-token" {
							t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						}
						var body struct {
							Argv    []string
							Timeout int `json:"timeout_seconds"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						_ = r.Body.Close()
						wantTimeout := 600
						if tc.timeout == "0" {
							wantTimeout = 0
						}
						if tc.timeout == "1" {
							wantTimeout = 1
						}
						if body.Timeout != wantTimeout || len(body.Argv) != 1 || body.Argv[0] != "echo" {
							t.Errorf("request body = %+v", body)
						}
						select {
						case <-r.Context().Done():
							return nil, r.Context().Err()
						case <-time.After(tc.delay):
							return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"run_id":"run-test","state":"exited","exit_code":0,"stdout":"result\n"}`)), Request: r}, nil
						}
					})
					args := []string{prefix, "run", "--ephemeral", "--rm", "--api-url", "http://run.invalid"}
					if tc.timeout != "" {
						args = append(args, "--timeout", tc.timeout)
					}
					args = append(args, "--", "echo")
					cmd := commands.NewRoot("test")
					var out, diagnostic bytes.Buffer
					cmd.SetOut(&out)
					cmd.SetErr(&diagnostic)
					cmd.SetArgs(args)
					ctx := t.Context()
					if tc.callerLimit > 0 {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, tc.callerLimit)
						defer cancel()
					}
					start := time.Now()
					err := cmd.ExecuteContext(ctx)
					if tc.invalid {
						if err == nil || !strings.Contains(err.Error(), "--timeout") || out.Len() != 0 || requests != 0 {
							t.Errorf("invalid timeout: error=%v output=%q requests=%d", err, out.String(), requests)
						}
						return
					}
					if tc.wantTimeout {
						if !errors.Is(err, context.DeadlineExceeded) || out.Len() != 0 {
							t.Errorf("deadline error=%v, output=%q", err, out.String())
						}
						if tc.callerLimit > 0 && time.Since(start) != tc.callerLimit {
							t.Errorf("caller deadline ignored: %s", time.Since(start))
						}
						if tc.callerLimit == 0 && time.Since(start) > 33*time.Minute {
							t.Errorf("stalled request exceeded lifecycle bound: %s", time.Since(start))
						}
					} else if err != nil || out.String() != "result\n" || time.Since(start) != tc.delay {
						t.Errorf("valid lifecycle: error=%v, output=%q, elapsed=%s, want %s", err, out.String(), time.Since(start), tc.delay)
					}
					if requests != 1 {
						t.Errorf("run requests=%d, want 1", requests)
					}
				})
			})
		}
	}
}
