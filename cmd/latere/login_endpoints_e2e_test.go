// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
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

func TestDeviceLoginUsesConfiguredEndpointsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"environment", "auth_flag", "api_flag", "both_flags"} {
		t.Run(mode, func(t *testing.T) {
			var misrouted, devices, actors, exchanges, verifications atomic.Int32
			// Reject all non-loopback traffic before TLS or token-bearing HTTP
			// requests can reach an external service, including on the buggy path.
			blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				misrouted.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer blocked.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/device/code":
					devices.Add(1)
					_, _ = w.Write([]byte(`{"device_code":"test-device","user_code":"TEST-CODE","verification_uri":"https://example.test/device","expires_in":60,"interval":1}`))
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"configured-auth","refresh_token":"configured-refresh","token_type":"Bearer","expires_in":3600}`))
				case "/actor-tokens":
					actors.Add(1)
					if r.Header.Get("Authorization") != "Bearer configured-auth" {
						t.Error("actor exchange did not use device token")
					}
					_, _ = w.Write([]byte(`{"actor_token":"configured-actor"}`))
				case "/v1/tokens/exchange":
					exchanges.Add(1)
					if r.Header.Get("Authorization") != "Bearer configured-actor" {
						t.Error("Cella exchange did not use actor token")
					}
					_, _ = w.Write([]byte(`{"access_token":"configured-cella"}`))
				case "/v1/sandboxes":
					verifications.Add(1)
					if r.Header.Get("Authorization") != "Bearer configured-cella" {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"code":"wrong_bearer"}`))
						return
					}
					_, _ = w.Write([]byte(`[]`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			apiEnv, authEnv := server.URL+"/", server.URL+"/"
			args := []string{"login", "--no-browser", "--no-git"}
			if mode == "auth_flag" || mode == "both_flags" {
				authEnv = blocked.URL
				args = append(args, "--auth-url", server.URL+"/")
			}
			if mode == "api_flag" || mode == "both_flags" {
				apiEnv = blocked.URL
				args = append(args, "--api-url", server.URL+"/")
			}
			root := t.TempDir()
			cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Stdin = strings.NewReader("")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath,
				"SANDBOX_API_URL="+apiEnv, "AUTH_URL="+authEnv, "XDG_CONFIG_HOME="+root,
				"HTTP_PROXY="+blocked.URL, "HTTPS_PROXY="+blocked.URL, "ALL_PROXY="+blocked.URL, "NO_PROXY=127.0.0.1,localhost",
				"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			out, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("configured login failed: %v\n%s", err, out)
			}
			if misrouted.Load() != 0 {
				t.Errorf("login made %d requests to an unconfigured endpoint", misrouted.Load())
			}
			if devices.Load() != 1 || actors.Load() != 1 || exchanges.Load() != 1 || verifications.Load() != 1 {
				t.Errorf("configured endpoint calls: device=%d actor=%d exchange=%d verify=%d; want one each", devices.Load(), actors.Load(), exchanges.Load(), verifications.Load())
			}
			if got, err := api.LoadToken(cellaPath); err != nil || got.AccessToken != "configured-cella" {
				t.Errorf("configured Cella token not saved: %v", err)
			}
			if got, err := api.LoadToken(authPath); err != nil || got.AccessToken != "configured-auth" {
				t.Errorf("configured auth token not saved: %v", err)
			}
		})
	}
}
