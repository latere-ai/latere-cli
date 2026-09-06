// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
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

func TestDriveDoesNotSubstituteCellaAfterAuthFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, kind := range []string{"git helper", "file command"} {
		// wantRoot is the root token auth must see on the Drive mint; wantBearer
		// is what Drive must then receive. A usable root always yields the
		// minted token; only a pasted login reaches Drive verbatim.
		for _, tc := range []struct {
			name, wantRoot, wantBearer string
			refreshStatus              int
		}{
			{name: "rejected refresh", refreshStatus: 400},
			{name: "unavailable refresh", refreshStatus: 503},
			{name: "malformed auth"},
			{name: "empty auth"},
			{name: "unreadable auth"},
			{name: "healthy auth", wantRoot: "root-access", wantBearer: "drive-actor"},
			{name: "refreshed auth", wantRoot: "new-root", wantBearer: "drive-actor", refreshStatus: 200},
			{name: "pasted token", wantBearer: "saved-cella"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				if err := api.SaveToken(cellaPath, api.Token{AccessToken: "saved-cella"}); err != nil {
					t.Fatal(err)
				}
				switch tc.name {
				case "pasted token": // Paste login removes the auth file.
				case "unreadable auth":
					if err := os.Mkdir(authPath, 0700); err != nil {
						t.Fatal(err)
					}
				case "malformed auth", "empty auth":
					contents := "{"
					if tc.name == "empty auth" {
						contents = "{}"
					}
					if err := os.WriteFile(authPath, []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
				default:
					expiry := time.Now().Add(time.Hour)
					if tc.refreshStatus != 0 {
						expiry = time.Now().Add(-time.Hour)
					}
					if err := api.SaveToken(authPath, api.Token{AccessToken: "root-access", RefreshToken: "root-refresh", ExpiresAt: expiry}); err != nil {
						t.Fatal(err)
					}
				}
				var refreshes, mints, driveCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/actor-tokens" {
						mints.Add(1)
						if tc.wantRoot == "" || r.Header.Get("Authorization") != "Bearer "+tc.wantRoot {
							t.Error("the Drive mint presented a substituted credential after auth failed")
						}
						_, _ = w.Write([]byte(`{"actor_token":"drive-actor","expires_in":300}`))
						return
					}
					if r.URL.Path == "/token" {
						refreshes.Add(1)
						if tc.refreshStatus == 0 {
							t.Error("unexpected refresh")
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						w.WriteHeader(tc.refreshStatus)
						if tc.refreshStatus == 200 {
							_, _ = w.Write([]byte(`{"access_token":"new-root","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
						} else {
							_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
						}
						return
					}
					driveCalls.Add(1)
					if tc.wantBearer == "" || r.Header.Get("Authorization") != "Bearer "+tc.wantBearer {
						t.Error("Drive received a substituted credential after auth failed")
					}
					_, _ = w.Write([]byte(`{"entries":[]}`))
				}))
				defer server.Close()
				args := []string{"git-credential", "get"}
				if kind == "file command" {
					args = []string{"drive", "ls"}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader("protocol=https\nhost=drive.latere.ai\n\n")
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "DRIVE_API_URL="+server.URL, "DRIVE_HOST=drive.latere.ai", "LATERE_DRIVE_TOKEN=", "AUTH_CLIENT_ID=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if kind == "file command" && tc.wantBearer == "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "latere login") {
						t.Errorf("failed Drive auth = %v; stderr: %s", err, stderr.String())
					}
				} else if err != nil {
					t.Errorf("command = %v; stderr: %s", err, stderr.String())
				}
				wantOut := ""
				if kind == "git helper" && tc.wantBearer != "" {
					wantOut = "username=token\npassword=" + tc.wantBearer + "\n\n"
				}
				if stdout.String() != wantOut {
					t.Errorf("stdout = %q, want %q", stdout.String(), wantOut)
				}
				var wantDrive, wantRefresh, wantMint int32
				if kind == "file command" && tc.wantBearer != "" {
					wantDrive = 1
				}
				if tc.refreshStatus != 0 {
					wantRefresh = 1
				}
				if tc.wantRoot != "" {
					wantMint = 1
				}
				if driveCalls.Load() != wantDrive || refreshes.Load() != wantRefresh || mints.Load() != wantMint {
					t.Errorf("requests: Drive=%d refresh=%d mint=%d, want %d/%d/%d", driveCalls.Load(), refreshes.Load(), mints.Load(), wantDrive, wantRefresh, wantMint)
				}
				if got, err := api.LoadToken(cellaPath); err != nil || got.AccessToken != "saved-cella" {
					t.Errorf("Drive changed Cella credentials: %v", err)
				}
			})
		}
	}
}
