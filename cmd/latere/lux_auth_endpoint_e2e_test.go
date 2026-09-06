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

func TestLuxAndDriveUseConfiguredAuthEndpointE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, flow := range []string{"lux identity", "lux refresh and actor", "lux actor", "git credential"} {
		for _, explicit := range []bool{false, true} {
			config := "environment"
			if explicit {
				config = "flag"
			}
			t.Run(flow+"/"+config, func(t *testing.T) {
				root := t.TempDir()
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				if err := api.SaveToken(cellaPath, api.Token{AccessToken: "saved-cella"}); err != nil {
					t.Fatal(err)
				}
				expires := time.Now().Add(-time.Hour)
				if flow == "lux actor" {
					expires = time.Now().Add(time.Hour)
				}
				if err := api.SaveToken(authPath, api.Token{AccessToken: "old-root", RefreshToken: "old-refresh", ExpiresAt: expires}); err != nil {
					t.Fatal(err)
				}
				var refreshes, mints, products, misrouted atomic.Int32
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
						if flow == "lux actor" {
							want = "Bearer old-root"
						}
						if r.Header.Get("Authorization") != want {
							t.Error("actor mint used the wrong root credential")
						}
						// Each product mints for its own audience; the git helper
						// is Drive-bound and receives a Drive token.
						var body struct {
							Audience string `json:"audience"`
						}
						_ = json.NewDecoder(r.Body).Decode(&body)
						wantAudience, actor := "lux.latere.ai", "lux-actor"
						if flow == "git credential" {
							wantAudience, actor = "drive.latere.ai", "drive-actor"
						}
						if body.Audience != wantAudience {
							t.Errorf("actor mint audience = %q, want %q", body.Audience, wantAudience)
						}
						_, _ = w.Write([]byte(`{"actor_token":"` + actor + `"}`))
					default:
						t.Errorf("unexpected auth endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer authServer.Close()
				productServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					products.Add(1)
					if r.URL.Path != "/lux/v1/rates" || r.Header.Get("Authorization") != "Bearer lux-actor" {
						t.Error("unexpected Lux request")
					}
					_, _ = w.Write([]byte(`{"items":[]}`))
				}))
				defer productServer.Close()
				authEnv := authServer.URL + "/"
				if explicit {
					authEnv = blocked.URL
				}
				args := []string{"lux", "env", "--raw"}
				wantOut := "new-root\n"
				var wantMints, wantProducts int32
				wantRefreshes := int32(1)
				switch flow {
				case "lux refresh and actor", "lux actor":
					args = []string{"lux", "rates", "--json"}
					wantOut = "[]\n"
					wantMints, wantProducts = 1, 1
					if flow == "lux actor" {
						wantRefreshes = 0
					}
				case "git credential":
					args = []string{"git-credential", "get"}
					wantOut = "username=token\npassword=drive-actor\n\n"
					wantMints = 1 // the refreshed root is only the bearer of the Drive mint
				}
				if explicit {
					args = append(args, "--auth-url", authServer.URL+"/")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader("protocol=https\nhost=drive.latere.ai\n\n")
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+authEnv, "LUX_API_URL="+productServer.URL, "DRIVE_HOST=drive.latere.ai", "LATERE_LUX_TOKEN=", "AUTH_CLIENT_ID=", "XDG_CONFIG_HOME="+root,
					"HTTP_PROXY="+blocked.URL, "HTTPS_PROXY="+blocked.URL, "ALL_PROXY="+blocked.URL, "NO_PROXY=127.0.0.1,localhost", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Run(); err != nil || stdout.String() != wantOut {
					t.Errorf("command = %v, stdout = %q; stderr: %s", err, stdout.String(), stderr.String())
				}
				if refreshes.Load() != wantRefreshes || mints.Load() != wantMints || products.Load() != wantProducts || misrouted.Load() != 0 {
					t.Errorf("requests: refresh=%d mint=%d product=%d unconfigured=%d; want %d/%d/%d/0", refreshes.Load(), mints.Load(), products.Load(), misrouted.Load(), wantRefreshes, wantMints, wantProducts)
				}
				if got, err := api.LoadToken(cellaPath); err != nil || got.AccessToken != "saved-cella" {
					t.Errorf("product authentication changed Cella credentials: %v", err)
				}
				if wantRefreshes != 0 {
					if got, err := api.LoadToken(authPath); err != nil || got.AccessToken != "new-root" || got.RefreshToken != "new-refresh" {
						t.Errorf("refreshed auth credential not saved: %v", err)
					}
				}
			})
		}
	}
}
