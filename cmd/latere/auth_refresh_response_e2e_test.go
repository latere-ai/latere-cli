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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthRefreshRequiresCompleteResponseE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, format := range []string{"json", "form"} {
		for _, state := range []string{"complete", "exact limit", "over limit", "truncated", "truncated at limit", "interrupted at limit"} {
			t.Run(format+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "auth-token.json")
				before, _ := json.Marshal(map[string]any{"access_token": "old-root", "refresh_token": "old-refresh", "client_id": "test-cli", "expires_at": time.Now().Add(-time.Hour)})
				if err := os.WriteFile(authPath, before, 0600); err != nil {
					t.Fatal(err)
				}
				payload := `{"access_token":"new-root","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`
				contentType, padding := "application/json", " "
				if format == "form" {
					contentType, padding = "application/x-www-form-urlencoded", "x"
					payload = "access_token=new-root&refresh_token=new-refresh&token_type=Bearer&expires_in=3600&padding="
				}
				if state != "complete" && state != "truncated" {
					payload += strings.Repeat(padding, (1<<20)-len(payload))
				}
				if state == "over limit" {
					payload += "trailing data beyond limit"
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/token" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					w.Header().Set("Content-Type", contentType)
					if state == "truncated" || state == "truncated at limit" {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					_, _ = w.Write([]byte(payload))
					if state == "interrupted at limit" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "lux", "env", "--raw", "--auth-url", server.URL)
				cmd.Env = append(os.Environ(), "LATERE_LUX_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				valid := state == "complete" || state == "exact limit"
				if valid {
					if err != nil || stdout.String() != "new-root\n" {
						t.Errorf("valid refresh failed: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "refresh failed") || stdout.Len() != 0 {
					t.Errorf("invalid refresh accepted: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
				after, err := os.ReadFile(authPath)
				if err != nil {
					t.Fatal(err)
				}
				if valid {
					var saved map[string]any
					if json.Unmarshal(after, &saved) != nil || saved["access_token"] != "new-root" || saved["refresh_token"] != "new-refresh" {
						t.Error("valid refreshed credentials not persisted")
					}
				} else if !bytes.Equal(before, after) {
					t.Error("invalid response replaced saved credentials")
				}
				if calls.Load() != 1 {
					t.Errorf("refresh calls=%d, want 1", calls.Load())
				}
			})
		}
	}
}
