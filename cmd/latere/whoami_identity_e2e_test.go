// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
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

func TestWhoamiRequiresIdentifiedPrincipalE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, reply := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"identified", `{"sub":"auth-owner","principal_type":"user","org_id":"auth-org"}`, 200, true},
		{"missing subject", `{"org_id":"unverified-org"}`, 200, false},
		{"empty subject", `{"sub":"","org_id":"unverified-org"}`, 200, false},
		{"null reply", `null`, 200, false},
		{"no content", ``, 204, false},
	} {
		t.Run(reply.name, func(t *testing.T) {
			root := t.TempDir()
			authPath := filepath.Join(root, "auth-token.json")
			t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"saved-owner","org_id":"saved-org"}`)) + ".signature"
			if err := api.SaveAuthToken(api.Token{AccessToken: token}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			var probes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("identity introspection used a different token")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/tokeninfo" {
					t.Errorf("whoami called %s; it speaks to the issuer alone", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				probes.Add(1)
				w.WriteHeader(reply.status)
				_, _ = w.Write([]byte(reply.body))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "whoami")
			command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+authPath,
				"AUTH_URL="+server.URL, "XDG_CONFIG_HOME="+root,
				"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			// An answer that identifies nobody falls back to the claims the
			// saved login itself carries; the command never fails over it.
			owner, org := "saved-owner", "saved-org"
			if reply.valid {
				owner, org = "auth-owner", "auth-org"
			}
			if err := command.Run(); err != nil || !strings.Contains(out.String(), owner) || !strings.Contains(out.String(), org) {
				t.Errorf("identity missing: %v: %s %s", err, out.String(), diagnostic.String())
			}
			if probes.Load() != 1 {
				t.Errorf("issuer requests=%d, want 1", probes.Load())
			}
			if data, err := os.ReadFile(authPath); err != nil || string(data) != string(before) {
				t.Errorf("the identity probe changed the saved login: %v", err)
			}
		})
	}
}
