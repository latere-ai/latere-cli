// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
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
)

func TestProductCommandsNeverRefreshCellaCredentialsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, product := range []string{"lux", "topos"} {
		for _, source := range []string{"override", "login", "expired login"} {
			for _, failure := range []bool{false, true} {
				name := product + "/" + source + "/expired Cella"
				if failure {
					name = product + "/" + source + "/product rejects bearer"
				}
				t.Run(name, func(t *testing.T) {
					root := t.TempDir()
					cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
					expiry := time.Now().Add(-time.Hour)
					if failure {
						expiry = time.Now().Add(time.Hour)
					}
					cellaBefore, _ := json.Marshal(map[string]any{"access_token": "saved-cella", "expires_at": expiry})
					authExpiry := time.Now().Add(time.Hour)
					if source == "expired login" {
						authExpiry = time.Now().Add(-time.Hour)
					}
					authBefore, _ := json.Marshal(map[string]any{"access_token": "auth-root", "refresh_token": "auth-refresh", "expires_at": authExpiry})
					for path, data := range map[string][]byte{cellaPath: cellaBefore, authPath: authBefore} {
						if err := os.WriteFile(path, data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					wantBearer := "product-override"
					if source != "override" {
						wantBearer = "auth-root"
						if source == "expired login" {
							wantBearer = "renewed-root"
						}
						if product == "lux" {
							wantBearer = "lux-actor"
						}
					}
					var cellaMints, exchanges, productCalls, luxMints, authRefreshes atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Path {
						case "/token":
							authRefreshes.Add(1)
							if err := r.ParseForm(); err != nil || r.PostForm.Get("refresh_token") != "auth-refresh" {
								t.Error("auth refresh used the wrong credential")
							}
							_, _ = w.Write([]byte(`{"access_token":"renewed-root","refresh_token":"renewed-refresh","token_type":"Bearer","expires_in":3600}`))
						case "/actor-tokens":
							var body struct {
								Audience string `json:"audience"`
							}
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if body.Audience == "lux.latere.ai" {
								luxMints.Add(1)
								_, _ = w.Write([]byte(`{"actor_token":"lux-actor"}`))
							} else {
								cellaMints.Add(1)
								_, _ = w.Write([]byte(`{"actor_token":"cella-actor"}`))
							}
						case "/v1/tokens/exchange":
							exchanges.Add(1)
							_, _ = w.Write([]byte(`{"access_token":"new-cella"}`))
						case "/lux/v1/rates", "/v1/agents":
							productCalls.Add(1)
							if got := r.Header.Get("Authorization"); got != "Bearer "+wantBearer {
								t.Errorf("product received %q, want its own bearer", got)
							}
							if failure {
								w.WriteHeader(http.StatusUnauthorized)
								_, _ = w.Write([]byte(`{"code":"product_rejected","message":"rejected product credential"}`))
							} else {
								_, _ = w.Write([]byte(`{"items":[],"agents":[]}`))
							}
						default:
							t.Errorf("unexpected endpoint: %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					defer server.Close()
					env := append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "SANDBOX_API_URL="+server.URL, "LUX_API_URL="+server.URL, "TOPOS_API_URL="+server.URL, "LATERE_LUX_TOKEN=", "TOPOS_TOKEN=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
					args := []string{"topos", "agents", "list"}
					if product == "lux" {
						args = []string{"lux", "rates", "--json", "--auth-url", server.URL}
					}
					if source == "override" {
						if product == "lux" {
							args = append(args, "--token", wantBearer)
						} else {
							env = append(env, "TOPOS_TOKEN="+wantBearer)
						}
					}
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = env
					out, err := command.CombinedOutput()
					if failure {
						if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "product_rejected") {
							t.Errorf("product rejection = %v: %s", err, out)
						}
					} else if err != nil {
						t.Errorf("product command = %v: %s", err, out)
					}
					if cellaMints.Load() != 0 || exchanges.Load() != 0 || productCalls.Load() != 1 {
						t.Errorf("requests: Cella mints=%d exchanges=%d product=%d, want 0/0/1", cellaMints.Load(), exchanges.Load(), productCalls.Load())
					}
					wantLuxMints := int32(0)
					if product == "lux" && source != "override" {
						wantLuxMints = 1
					}
					if luxMints.Load() != wantLuxMints {
						t.Errorf("Lux actor mint calls = %d, want %d", luxMints.Load(), wantLuxMints)
					}
					wantRefreshes := int32(0)
					if source == "expired login" {
						wantRefreshes = 1
					}
					if authRefreshes.Load() != wantRefreshes {
						t.Errorf("auth refresh calls = %d, want %d", authRefreshes.Load(), wantRefreshes)
					}
					for path, before := range map[string][]byte{cellaPath: cellaBefore, authPath: authBefore} {
						if source == "expired login" && path == authPath {
							data, err := os.ReadFile(path)
							var saved map[string]any
							if err != nil || json.Unmarshal(data, &saved) != nil || saved["access_token"] != "renewed-root" || saved["refresh_token"] != "renewed-refresh" {
								t.Errorf("renewed auth credential not saved: %v", err)
							}
							continue
						}
						if data, err := os.ReadFile(path); err != nil || string(data) != string(before) {
							t.Errorf("product command changed saved %s: %v", filepath.Base(path), err)
						}
					}
				})
			}
		}
	}
}
