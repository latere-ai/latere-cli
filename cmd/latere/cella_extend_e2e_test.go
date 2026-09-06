// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
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

func TestCellaExtendLifetimeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	tokenPath := filepath.Join(root, "token.json")
	if err := os.WriteFile(tokenPath, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	const deadline = "2099-01-02T03:04:05Z"
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, tc := range []struct {
			name     string
			flags    []string
			hours    int
			deadline string
			wantErr  string
		}{
			{name: "default", hours: 24},
			{name: "positive", flags: []string{"--hours", "72"}, hours: 72},
			{name: "zero", flags: []string{"--hours", "0"}, wantErr: "--hours must be greater than zero"},
			{name: "negative", flags: []string{"--hours", "-1"}, wantErr: "--hours must be greater than zero"},
			{name: "empty deadline", flags: []string{"--deadline", ""}, wantErr: "--deadline must be RFC3339"},
			{name: "malformed deadline", flags: []string{"--deadline", "not-a-date"}, wantErr: "--deadline must be RFC3339"},
			{name: "zero deadline", flags: []string{"--deadline", "0001-01-01T00:00:00Z"}, wantErr: "--deadline must be in the future"},
			{name: "past deadline", flags: []string{"--deadline", "2000-01-01T00:00:00Z"}, wantErr: "--deadline must be in the future"},
			{name: "future deadline", flags: []string{"--deadline", deadline}, deadline: deadline},
			{name: "offset deadline", flags: []string{"--deadline", "2099-01-02T05:04:05+02:00"}, deadline: "2099-01-02T05:04:05+02:00"},
			{name: "deadline overrides hours", flags: []string{"--hours", "-1", "--deadline", deadline}, deadline: deadline},
		} {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/dev/extend" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					var body struct {
						Hours    int    `json:"auto_delete_hours"`
						Deadline string `json:"deadline"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if tc.wantErr == "" && (body.Hours != tc.hours || body.Deadline != tc.deadline) {
						t.Errorf("extend body = %+v; want hours %d, deadline %q", body, tc.hours, tc.deadline)
					}
					_, _ = w.Write([]byte(`{"id":"dev","state":"running"}`))
				}))
				defer server.Close()
				args := append([]string{prefix, "extend", "dev", "--api-url", server.URL}, tc.flags...)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				out, err := command.CombinedOutput()
				if tc.wantErr != "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), tc.wantErr) {
						t.Errorf("invalid lifetime returned %v: %s", err, out)
					}
					if requests.Load() != 0 {
						t.Errorf("invalid lifetime made %d requests", requests.Load())
					}
				} else if err != nil || requests.Load() != 1 {
					t.Errorf("valid extend returned %v, requests %d: %s", err, requests.Load(), out)
				}
			})
		}
	}
}
