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

func TestProductsRejectExpiredAuthWithoutRefreshE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, product := range []string{"lux export", "git helper", "drive", "topos", "cella"} {
		for _, state := range []string{"expired", "near expiry", "unknown expiry"} {
			t.Run(product+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "auth-token.json")
				t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
				var expiry time.Time
				switch state {
				case "expired":
					expiry = time.Now().Add(-time.Hour)
				case "near expiry":
					expiry = time.Now().Add(45 * time.Second)
				}
				if err := api.SaveAuthToken(api.Token{AccessToken: "saved-auth", ExpiresAt: expiry}); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(authPath)
				if err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					// Every product first mints an actor token from the usable
					// login, presenting it to auth alone, then presents the
					// minted token to the product.
					if r.Method == http.MethodPost && r.URL.Path == "/actor-tokens" {
						if r.Header.Get("Authorization") != "Bearer saved-auth" {
							t.Error("unexpected actor mint or credential")
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"actor_token":"product-actor","expires_in":300}`))
						return
					}
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer product-actor" {
						t.Error("unexpected product request or credential")
					}
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/v1/sandboxes" {
						_, _ = w.Write([]byte(`[]`))
						return
					}
					_, _ = w.Write([]byte(`{"entries":[],"agents":[]}`))
				}))
				defer server.Close()
				args := []string{"lux", "env", "--raw"}
				switch product {
				case "git helper":
					args = []string{"git-credential", "get"}
				case "drive":
					args = []string{"drive", "ls"}
				case "topos":
					args = []string{"topos", "agents", "list"}
				case "cella":
					args = []string{"cella", "list", "--api-url", server.URL}
					// SANDBOX_API_URL and AUTH_URL both point at the stub, so
					// the mint and the product call reach the same server.
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader("protocol=https\nhost=code.latere.ai\n\n")
				command.Env = append(os.Environ(), "LATERE_CELLA_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "DRIVE_API_URL="+server.URL, "TOPOS_API_URL="+server.URL, "LUX_API_URL="+server.URL, "LATERE_DRIVE_TOKEN=", "LATERE_LUX_TOKEN=", "TOPOS_TOKEN=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err = command.Run()
				if state == "expired" {
					const wantError = "latere login"
					var wantRequests int32
					if product == "git helper" {
						if err != nil {
							t.Errorf("git credential miss must be quiet: %v", err)
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), wantError) {
						t.Errorf("expired credential = %v; stderr: %s", err, stderr.String())
					}
					if stdout.Len() != 0 || requests.Load() != wantRequests {
						t.Errorf("used expired credential: stdout=%q requests=%d", stdout.String(), requests.Load())
					}
				} else {
					if err != nil {
						t.Errorf("usable credential rejected: %v; stderr: %s", err, stderr.String())
					}
					// One mint per product, then one product call; the two
					// export commands hand their mint's result to the caller
					// and make no product call.
					wantRequests := int32(2)
					switch product {
					case "lux export", "git helper":
						wantRequests = 1
						if !strings.Contains(stdout.String(), "product-actor") || strings.Contains(stdout.String(), "saved-auth") {
							t.Errorf("%s credential = %q, want the minted actor token and never the root", product, stdout.String())
						}
					}
					if requests.Load() != wantRequests {
						t.Errorf("product requests = %d, want %d", requests.Load(), wantRequests)
					}
				}
				if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, before) {
					t.Errorf("credential resolution changed %s: %v", filepath.Base(authPath), err)
				}
			})
		}
	}
}
