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
	for _, kind := range []string{"issuer_answers", "issuer_refuses"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			authPath := filepath.Join(root, "auth-token.json")
			t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
			token := "synthetic-opaque-auth-token"
			if kind == "issuer_refuses" {
				token = "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"test-owner","principal_type":"user","org_id":"test-org"}`)) + ".signature"
			}
			if err := api.SaveAuthToken(api.Token{AccessToken: token}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			var probes, misrouted atomic.Int32
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
				if kind == "issuer_refuses" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"code":"invalid_token"}`))
					return
				}
				_, _ = w.Write([]byte(`{"sub":"test-owner","email":"owner@example.test","principal_type":"user","org_id":"test-org","scopes":["openid"],"client_id":"latere-cli"}`))
			}))
			defer authServer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "whoami")
			command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+authPath,
				"AUTH_URL="+authServer.URL+"/", "XDG_CONFIG_HOME="+root,
				"HTTP_PROXY="+blocked.URL, "HTTPS_PROXY="+blocked.URL, "ALL_PROXY="+blocked.URL, "NO_PROXY=127.0.0.1,localhost",
				"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			out, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("whoami: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "test-owner") || !strings.Contains(string(out), "test-org") {
				t.Errorf("missing configured identity: %s", out)
			}
			if probes.Load() != 1 || misrouted.Load() != 0 {
				t.Errorf("requests: issuer=%d unconfigured=%d; want 1 and 0", probes.Load(), misrouted.Load())
			}
			if data, err := os.ReadFile(authPath); err != nil || string(data) != string(before) {
				t.Errorf("the identity probe changed the saved login: %v", err)
			}
		})
	}
}
