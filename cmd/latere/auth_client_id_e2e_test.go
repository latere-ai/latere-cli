// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestLoginRetainsOAuthClientAcrossSessionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, clientID := range []string{"latere-cli", "custom-cli"} {
		t.Run(clientID, func(t *testing.T) {
			root := t.TempDir()
			cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
			var expireCella atomic.Bool
			var exchanges, refreshes, revocations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/device/code" || r.URL.Path == "/token" || r.URL.Path == "/revoke" {
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if got := r.PostForm.Get("client_id"); got != clientID {
						t.Errorf("%s client_id = %q, want %q", r.URL.Path, got, clientID)
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
						return
					}
				}
				switch r.URL.Path {
				case "/device/code":
					_, _ = w.Write([]byte(`{"device_code":"test-device","user_code":"TEST-CODE","verification_uri":"https://example.test/device","expires_in":60,"interval":1}`))
				case "/token":
					if r.PostForm.Get("grant_type") == "refresh_token" {
						refreshes.Add(1)
						if got := r.PostForm.Get("refresh_token"); got != "initial-refresh" && got != "renewed-refresh" {
							t.Error("refresh used unexpected token")
						}
						_, _ = w.Write([]byte(`{"access_token":"renewed-root","refresh_token":"renewed-refresh","token_type":"Bearer","expires_in":3600}`))
					} else {
						_, _ = w.Write([]byte(`{"access_token":"initial-root","refresh_token":"initial-refresh","token_type":"Bearer","expires_in":1}`))
					}
				case "/actor-tokens":
					_, _ = w.Write([]byte(`{"actor_token":"test-actor"}`))
				case "/v1/tokens/exchange":
					n := exchanges.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fmt.Sprintf("cella-%d", n)})
				case "/v1/sandboxes":
					if expireCella.Load() && r.Header.Get("Authorization") == "Bearer cella-1" {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"code":"expired_token"}`))
						return
					}
					_, _ = w.Write([]byte(`[]`))
				case "/v1/tokens/current":
					w.WriteHeader(http.StatusNoContent)
				case "/revoke":
					revocations.Add(1)
					if r.PostForm.Get("token") != "renewed-refresh" {
						t.Error("logout did not revoke the latest refresh token")
					}
					w.WriteHeader(http.StatusOK)
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			env := append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "SANDBOX_API_URL="+server.URL, "AUTH_CLIENT_ID=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			run := func(args ...string) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader("")
				command.Env = env
				if out, err := command.CombinedOutput(); err != nil {
					t.Errorf("%s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
			run("login", "--no-browser", "--no-git", "--client-id", clientID)
			// Later environment changes must not rebind an existing refresh token.
			env = append(env, "AUTH_CLIENT_ID=unrelated-cli")
			expireCella.Store(true)
			run("cella", "list")
			run("org", "new-org")
			run("logout")
			if refreshes.Load() != 2 || exchanges.Load() != 3 || revocations.Load() != 1 {
				t.Errorf("session calls: refresh=%d exchange=%d revoke=%d, want 2/3/1", refreshes.Load(), exchanges.Load(), revocations.Load())
			}
			for _, path := range []string{cellaPath, authPath} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("logout retained %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}
