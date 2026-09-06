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

func TestAuthRefreshPreservesRequestOnRedirectE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, status := range []int{200, 301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			root := t.TempDir()
			authPath := filepath.Join(root, "auth-token.json")
			before, _ := json.Marshal(map[string]any{"access_token": "old-root", "refresh_token": "old-refresh", "client_id": "test-cli", "expires_at": time.Now().Add(-time.Hour)})
			if err := os.WriteFile(authPath, before, 0600); err != nil {
				t.Fatal(err)
			}
			var calls, redirects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/token" && status != 200 {
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(status)
					return
				}
				if r.URL.Path == "/redirected" {
					redirects.Add(1)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Method != http.MethodPost || r.PostForm.Get("refresh_token") != "old-refresh" || r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("client_id") != "test-cli" || r.Header.Get("Authorization") != "" {
					t.Errorf("refresh request lost its method or form: %s %s %v", r.Method, r.URL.Path, r.PostForm)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"new-root","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "lux", "env", "--raw", "--auth-url", server.URL)
			cmd.Env = append(os.Environ(), "LATERE_LUX_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "AUTH_CLIENT_ID=unrelated-client", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			denied := status == 301 || status == 302 || status == 303
			if denied {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "redirect") || stdout.Len() != 0 {
					t.Errorf("method-changing redirect accepted: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
			} else if err != nil || stdout.String() != "new-root\n" {
				t.Errorf("valid refresh failed: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			after, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			if denied {
				if !bytes.Equal(before, after) {
					t.Error("rejected refresh replaced saved credentials")
				}
			} else {
				var saved map[string]any
				if err := json.Unmarshal(after, &saved); err != nil || saved["access_token"] != "new-root" || saved["refresh_token"] != "new-refresh" || saved["client_id"] != "test-cli" {
					t.Errorf("refreshed credentials were not persisted correctly: %v", err)
				}
			}
			wantRedirects := int32(0)
			if status == 307 || status == 308 {
				wantRedirects = 1
			}
			if redirects.Load() != wantRedirects || calls.Load() != 1+wantRedirects {
				t.Errorf("requests=%d redirects=%d, want %d/%d", calls.Load(), redirects.Load(), 1+wantRedirects, wantRedirects)
			}
		})
	}
}
