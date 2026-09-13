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
	for _, mode := range []string{"environment", "auth_flag"} {
		t.Run(mode, func(t *testing.T) {
			var misrouted, devices, approvals atomic.Int32
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
					approvals.Add(1)
					_, _ = w.Write([]byte(`{"access_token":"configured-auth","refresh_token":"configured-refresh","token_type":"Bearer","expires_in":3600}`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			authEnv := server.URL + "/"
			args := []string{"login", "--no-browser", "--no-git"}
			if mode == "auth_flag" {
				authEnv = blocked.URL
				args = append(args, "--auth-url", server.URL+"/")
			}
			root := t.TempDir()
			authPath := filepath.Join(root, "auth-token.json")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Stdin = strings.NewReader("")
			command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+authPath,
				"AUTH_URL="+authEnv, "XDG_CONFIG_HOME="+root,
				"HTTP_PROXY="+blocked.URL, "HTTPS_PROXY="+blocked.URL, "ALL_PROXY="+blocked.URL, "NO_PROXY=127.0.0.1,localhost",
				"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			out, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("configured login failed: %v\n%s", err, out)
			}
			if misrouted.Load() != 0 {
				t.Errorf("login made %d requests to an unconfigured endpoint", misrouted.Load())
			}
			if devices.Load() != 1 || approvals.Load() != 1 {
				t.Errorf("configured endpoint calls: device=%d token=%d; want one each", devices.Load(), approvals.Load())
			}
			t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
			if got, err := api.LoadAuthToken(); err != nil || got.AccessToken != "configured-auth" {
				t.Errorf("configured login token not saved: %v", err)
			}
		})
	}
}
