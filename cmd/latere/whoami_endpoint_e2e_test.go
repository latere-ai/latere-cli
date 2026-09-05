// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/base64"
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

func TestWhoamiUsesConfiguredAuthEndpointE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, kind := range []string{"auth_token", "cella_token"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			tokenPath := filepath.Join(root, "token.json")
			token := "synthetic-opaque-auth-token"
			if kind == "cella_token" {
				token = "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"test-owner","principal_type":"user","scope":"read:sandbox","org_id":"test-org"}`)) + ".signature"
			}
			if err := api.SaveToken(tokenPath, api.Token{AccessToken: token}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(tokenPath)
			if err != nil {
				t.Fatal(err)
			}
			var probes, verifications, misrouted atomic.Int32
			// Capture and reject any accidentally inferred external endpoint.
			blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				misrouted.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer blocked.Close()
			authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes.Add(1)
				if r.URL.Path != "/tokeninfo" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("incorrect auth introspection request")
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "cella_token" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"code":"not_auth_issued"}`))
					return
				}
				_, _ = w.Write([]byte(`{"sub":"test-owner","email":"owner@example.test","principal_type":"user","org_id":"test-org","scopes":["openid"],"client_id":"latere-cli"}`))
			}))
			defer authServer.Close()
			cellaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				verifications.Add(1)
				if r.URL.Path != "/v1/sandboxes" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("incorrect Cella verification request")
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "auth_token" {
					w.WriteHeader(http.StatusUnauthorized)
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			defer cellaServer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "whoami")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"),
				"SANDBOX_API_URL="+cellaServer.URL, "AUTH_URL="+authServer.URL+"/", "XDG_CONFIG_HOME="+root,
				"HTTP_PROXY="+blocked.URL, "HTTPS_PROXY="+blocked.URL, "ALL_PROXY="+blocked.URL, "NO_PROXY=127.0.0.1,localhost",
				"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			out, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("whoami: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "test-owner") || !strings.Contains(string(out), "test-org") {
				t.Errorf("missing configured identity: %s", out)
			}
			wantVerifications := int32(0)
			if kind == "cella_token" {
				wantVerifications = 1
			}
			if probes.Load() != 1 || verifications.Load() != wantVerifications || misrouted.Load() != 0 {
				t.Errorf("requests: auth=%d Cella=%d unconfigured=%d; want auth=1 Cella=%d unconfigured=0", probes.Load(), verifications.Load(), misrouted.Load(), wantVerifications)
			}
			if data, err := os.ReadFile(tokenPath); err != nil || string(data) != string(before) {
				t.Errorf("identity probe changed stored token: %v", err)
			}
		})
	}
}
