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

// Device-code login speaks to the issuer and to nobody else, and it
// replaces the saved login only once the approval has landed. A failed
// save leaves the previous login intact rather than a half-written one.
func TestDeviceLoginSavesOnlyTheApprovedTokenE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name      string
		blockSave bool
	}{
		{"success", false},
		{"save_failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			authDir := filepath.Join(root, "auth")
			if err := os.Mkdir(authDir, 0700); err != nil {
				t.Fatal(err)
			}
			authPath := filepath.Join(authDir, "auth-token.json")
			const before = `{"access_token":"old-auth","refresh_token":"old-refresh"}`
			if err := os.WriteFile(authPath, []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.blockSave {
				makeTokenDirectoryReadOnly(t, authDir)
			}
			// Until the approval lands the saved login is untouched.
			assertUnchanged := func() {
				data, err := os.ReadFile(authPath)
				if err != nil || string(data) != before {
					t.Errorf("the saved login changed before the approval landed: %v", err)
				}
			}
			var approvals atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/device/code":
					assertUnchanged()
					_, _ = w.Write([]byte(`{"device_code":"test-device","user_code":"TEST-CODE","verification_uri":"https://example.test/device","expires_in":60,"interval":1}`))
				case "/token":
					approvals.Add(1)
					assertUnchanged()
					_, _ = w.Write([]byte(`{"access_token":"new-auth","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
				default:
					t.Errorf("login called %s; it speaks to the issuer alone", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "login", "--no-browser", "--no-git", "--auth-url", server.URL)
			command.Stdin = strings.NewReader("")
			command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			if approvals.Load() != 1 {
				t.Errorf("token-endpoint calls = %d, want 1: %s", approvals.Load(), out)
			}
			if tc.blockSave {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
					t.Errorf("persistence failure exit = %v: %s", err, out)
				}
				if strings.Contains(string(out), "Logged in.") {
					t.Errorf("failed login reported success: %s", out)
				}
				assertUnchanged()
				return
			}
			if err != nil {
				t.Fatalf("login: %v\n%s", err, out)
			}
			t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
			got, err := api.LoadAuthToken()
			if err != nil || got.AccessToken != "new-auth" || got.RefreshToken != "new-refresh" || got.ExpiresAt.IsZero() {
				t.Errorf("successful login did not retain the approved token and refresh grant: %+v (%v)", got, err)
			}
		})
	}
}
