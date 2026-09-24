// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// gitCredentialRequest is what git writes to the credential helper for the
// code host, and gitCredentialAnswer the helper's answer when auth mints
// "code-actor".
const (
	gitCredentialRequest = "protocol=https\nhost=code.latere.ai\n\n"
	gitCredentialAnswer  = "username=x-access-token\npassword=code-actor\n\n"
)

// driveLs is the product command the refresh tests run against server, which
// is both auth and Drive: `drive ls` refreshes the saved login when it is
// due, mints a Drive token with it, presents that token to Drive, and reports
// any failure on stderr with a non-zero exit.
func driveLs(server string) []string {
	return []string{"drive", "--drive-url", server, "--auth-url", server, "ls"}
}

// serveDriveList answers a Drive listing on the refresh tests' stub, and
// reports whether r was one. The listing must present the token auth minted.
func serveDriveList(t *testing.T, w http.ResponseWriter, r *http.Request, products *atomic.Int32) bool {
	t.Helper()
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1/") {
		return false
	}
	products.Add(1)
	if r.Header.Get("Authorization") != "Bearer drive-actor" {
		t.Errorf("Drive received %q, want the minted Drive token", r.Header.Get("Authorization"))
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"entries":[]}`))
	return true
}

// TestProductMintUsesConfiguredAuthEndpointE2E: the refresh and the mint a
// product command makes go to the auth endpoint AUTH_URL or --auth-url names,
// and nowhere else, not even through a configured proxy.
func TestProductMintUsesConfiguredAuthEndpointE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	for _, flow := range []string{"refresh and mint", "mint"} {
		for _, explicit := range []bool{false, true} {
			config := "environment"
			if explicit {
				config = "flag"
			}
			t.Run(flow+"/"+config, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "auth-token.json")
				t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
				expires := time.Now().Add(-time.Hour)
				if flow == "mint" {
					expires = time.Now().Add(time.Hour)
				}
				if err := api.SaveAuthToken(api.Token{AccessToken: "old-root", RefreshToken: "old-refresh", ExpiresAt: expires}); err != nil {
					t.Fatal(err)
				}
				var refreshes, mints, misrouted atomic.Int32
				blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					misrouted.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer blocked.Close()
				authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/token":
						refreshes.Add(1)
						if err := r.ParseForm(); err != nil || r.PostForm.Get("refresh_token") != "old-refresh" {
							t.Error("unexpected refresh credential")
						}
						_, _ = w.Write([]byte(`{"access_token":"new-root","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
					case "/actor-tokens":
						mints.Add(1)
						want := "Bearer new-root"
						if flow == "mint" {
							want = "Bearer old-root"
						}
						if r.Header.Get("Authorization") != want {
							t.Error("actor mint used the wrong root credential")
						}
						// The git helper is Origo-bound and receives an Origo token.
						var body struct {
							Audience string `json:"audience"`
						}
						_ = json.NewDecoder(r.Body).Decode(&body)
						if body.Audience != "origo" {
							t.Errorf("actor mint audience = %q, want origo", body.Audience)
						}
						_, _ = w.Write([]byte(`{"actor_token":"code-actor","expires_in":300}`))
					default:
						t.Errorf("unexpected auth endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer authServer.Close()
				authEnv := authServer.URL + "/"
				if explicit {
					authEnv = blocked.URL
				}
				args := []string{"git-credential", "get"}
				if explicit {
					args = append(args, "--auth-url", authServer.URL+"/")
				}
				wantRefreshes := int32(1)
				if flow == "mint" {
					wantRefreshes = 0
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader(gitCredentialRequest)
				command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+authEnv, "AUTH_CLIENT_ID=", "XDG_CONFIG_HOME="+root,
					"HTTP_PROXY="+blocked.URL, "HTTPS_PROXY="+blocked.URL, "ALL_PROXY="+blocked.URL, "NO_PROXY=127.0.0.1,localhost", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Run(); err != nil || stdout.String() != gitCredentialAnswer {
					t.Errorf("command = %v, stdout = %q; stderr: %s", err, stdout.String(), stderr.String())
				}
				if refreshes.Load() != wantRefreshes || mints.Load() != 1 || misrouted.Load() != 0 {
					t.Errorf("requests: refresh=%d mint=%d unconfigured=%d; want %d/1/0", refreshes.Load(), mints.Load(), misrouted.Load(), wantRefreshes)
				}
				if wantRefreshes != 0 {
					if got, err := api.LoadAuthToken(); err != nil || got.AccessToken != "new-root" || got.RefreshToken != "new-refresh" {
						t.Errorf("refreshed login not saved: %v", err)
					}
				}
			})
		}
	}
}
