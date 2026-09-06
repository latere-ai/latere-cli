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

func TestAuthRefreshRequiresPersistenceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, rotation := range []string{"rotated", "retained"} {
		for _, mode := range []string{"writable", "blocked parent", "directory target"} {
			t.Run(rotation+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				parent := filepath.Join(root, "credentials")
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				authPath := filepath.Join(parent, "auth-token.json")
				before, _ := json.Marshal(map[string]any{"access_token": "old-root", "refresh_token": "old-refresh", "client_id": "test-cli", "expires_at": time.Now().Add(-time.Hour)})
				if err := os.WriteFile(authPath, before, 0600); err != nil {
					t.Fatal(err)
				}
				backup := filepath.Join(root, "old-auth.json")
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/token" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					// The CLI has already loaded the old token. Make only this
					// test's credential path unwritable before returning the refresh.
					if mode != "writable" {
						if err := os.Rename(authPath, backup); err != nil {
							t.Error(err)
							return
						}
						if mode == "directory target" {
							if err := os.Mkdir(authPath, 0700); err != nil {
								t.Error(err)
								return
							}
						} else {
							if err := os.Remove(parent); err != nil {
								t.Error(err)
								return
							}
							if err := os.WriteFile(parent, []byte("keep"), 0600); err != nil {
								t.Error(err)
								return
							}
						}
					}
					payload := map[string]any{"access_token": "new-root", "token_type": "Bearer", "expires_in": 3600}
					if rotation == "rotated" {
						payload["refresh_token"] = "new-refresh"
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(payload)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "lux", "env", "--raw", "--auth-url", server.URL)
				cmd.Env = append(os.Environ(), "LATERE_LUX_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				if mode == "writable" {
					if err != nil || stdout.String() != "new-root\n" {
						t.Errorf("valid refresh failed: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
					data, readErr := os.ReadFile(authPath)
					var saved map[string]any
					wantRefresh := "old-refresh"
					if rotation == "rotated" {
						wantRefresh = "new-refresh"
					}
					if readErr != nil || json.Unmarshal(data, &saved) != nil || saved["access_token"] != "new-root" || saved["refresh_token"] != wantRefresh {
						t.Errorf("new credentials not saved correctly: %v", readErr)
					}
				} else {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "save refreshed auth token") || !strings.Contains(stderr.String(), "latere login") || stdout.Len() != 0 {
						t.Errorf("save failure not reported: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
					if data, err := os.ReadFile(backup); err != nil || !bytes.Equal(data, before) {
						t.Errorf("previous credential changed: %v", err)
					}
				}
				if calls.Load() != 1 {
					t.Errorf("refresh calls=%d, want 1", calls.Load())
				}
			})
		}
	}
}
