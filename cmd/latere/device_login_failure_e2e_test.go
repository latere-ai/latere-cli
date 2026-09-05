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

func TestDeviceLoginPreservesSessionUntilCellaSucceedsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name                      string
		actorStatus, verifyStatus int
		blockSave, blockAuthSave  bool
	}{
		{"exchange_and_verification_rejected", 503, 401, false, false},
		{"verification_rejected", 200, 401, false, false},
		{"verification_unavailable", 200, 503, false, false},
		{"cella_save_failed", 200, 200, true, false},
		{"auth_save_failed", 200, 200, false, true},
		{"success", 200, 200, false, false},
		{"legacy_success", 503, 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			authDir := filepath.Join(root, "auth")
			if err := os.Mkdir(authDir, 0700); err != nil {
				t.Fatal(err)
			}
			cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(authDir, "auth-token.json")
			before := map[string]string{cellaPath: `{"access_token":"old-cella"}`, authPath: `{"access_token":"old-auth","refresh_token":"old-refresh"}`}
			for path, contents := range before {
				if tc.blockSave && path == cellaPath {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.blockAuthSave {
				makeTokenDirectoryReadOnly(t, authDir)
			}
			assertUnchanged := func() {
				for path, contents := range before {
					if tc.blockSave && path == cellaPath {
						if info, err := os.Stat(path); err != nil || !info.IsDir() {
							t.Error("failed save changed destination directory")
						}
						continue
					}
					data, err := os.ReadFile(path)
					if err != nil || string(data) != contents {
						t.Errorf("saved %s changed before successful Cella login: %v", filepath.Base(path), err)
					}
				}
			}
			candidate := "new-cella"
			if tc.actorStatus != 200 {
				candidate = "new-auth"
			}
			var verifications atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/device/code":
					_, _ = w.Write([]byte(`{"device_code":"test-device","user_code":"TEST-CODE","verification_uri":"https://example.test/device","expires_in":60,"interval":1}`))
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"new-auth","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
				case "/actor-tokens":
					assertUnchanged()
					if r.Header.Get("Authorization") != "Bearer new-auth" {
						t.Error("actor exchange did not use candidate auth token")
					}
					w.WriteHeader(tc.actorStatus)
					if tc.actorStatus == 200 {
						_, _ = w.Write([]byte(`{"actor_token":"test-actor"}`))
					}
				case "/v1/tokens/exchange":
					_, _ = w.Write([]byte(`{"access_token":"new-cella"}`))
				case "/v1/sandboxes":
					verifications.Add(1)
					assertUnchanged()
					if r.Header.Get("Authorization") != "Bearer "+candidate {
						t.Error("verification did not use candidate token")
					}
					w.WriteHeader(tc.verifyStatus)
					if tc.verifyStatus == 200 {
						_, _ = w.Write([]byte(`[]`))
					} else {
						_, _ = w.Write([]byte(`{"code":"verification_failed"}`))
					}
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "login", "--no-browser", "--no-git", "--api-url", server.URL, "--auth-url", server.URL)
			command.Stdin = strings.NewReader("")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			if verifications.Load() != 1 {
				t.Errorf("verification calls = %d, want 1: %s", verifications.Load(), out)
			}
			if tc.blockAuthSave {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
					t.Errorf("auth persistence failure exit = %v: %s", err, out)
				}
				if strings.Contains(string(out), "Logged in.") {
					t.Errorf("failed login reported success: %s", out)
				}
				if _, err := os.Stat(cellaPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("auth save failure retained the new account's Cella token: %v", err)
				}
				data, err := os.ReadFile(authPath)
				if err != nil || string(data) != before[authPath] {
					t.Errorf("failed auth save modified previous root credential: %v", err)
				}
				return
			}
			if tc.verifyStatus != 200 || tc.blockSave {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
					t.Errorf("failed login exit = %v: %s", err, out)
				}
				assertUnchanged()
				return
			}
			if err != nil {
				t.Fatalf("login: %v\n%s", err, out)
			}
			if got, err := api.LoadToken(cellaPath); err != nil || got.AccessToken != candidate {
				t.Errorf("successful login did not save candidate Cella token: %v", err)
			}
			if got, err := api.LoadToken(authPath); err != nil || got.AccessToken != "new-auth" || got.RefreshToken != "new-refresh" || got.ExpiresAt.IsZero() {
				t.Errorf("successful login did not retain auth token and refresh grant: %v", err)
			}
		})
	}
}
