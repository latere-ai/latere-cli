// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
		for _, allowed := range []bool{false, true} {
			name := "fallback rejected"
			if allowed {
				name = "fallback accepted"
			}
			t.Run(reply.name+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				tokenPath := filepath.Join(root, "token.json")
				token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"cella-owner","org_id":"cella-org"}`)) + ".signature"
				if err := api.SaveToken(tokenPath, api.Token{AccessToken: token}); err != nil {
					t.Fatal(err)
				}
				var probes, verifications atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+token {
						t.Error("identity verification used a different token")
					}
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/tokeninfo":
						probes.Add(1)
						w.WriteHeader(reply.status)
						_, _ = w.Write([]byte(reply.body))
					case "/v1/sandboxes":
						verifications.Add(1)
						if !allowed {
							w.WriteHeader(http.StatusUnauthorized)
							_, _ = w.Write([]byte(`{"code":"invalid_token"}`))
							return
						}
						_, _ = w.Write([]byte(`[]`))
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "whoami")
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "missing-auth.json"),
					"AUTH_URL="+server.URL, "SANDBOX_API_URL="+server.URL, "XDG_CONFIG_HOME="+root,
					"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				if !reply.valid && !allowed {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "invalid_token") {
						t.Errorf("unverified identity returned %v: %s", err, diagnostic.String())
					}
					if out.Len() != 0 {
						t.Errorf("unverified identity was printed: %s", out.String())
					}
				} else {
					owner, org := "cella-owner", "cella-org"
					if reply.valid {
						owner, org = "auth-owner", "auth-org"
					}
					if err != nil || !strings.Contains(out.String(), owner) || !strings.Contains(out.String(), org) {
						t.Errorf("verified identity missing: %v: %s %s", err, out.String(), diagnostic.String())
					}
				}
				wantVerifications := int32(1)
				if reply.valid {
					wantVerifications = 0
				}
				if probes.Load() != 1 || verifications.Load() != wantVerifications {
					t.Errorf("auth/Cella requests=%d/%d, want 1/%d", probes.Load(), verifications.Load(), wantVerifications)
				}
			})
		}
	}
}
