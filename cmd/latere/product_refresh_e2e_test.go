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
	binary := latereBinary(t)
	for _, source := range []string{"override", "login", "expired login"} {
		for _, failure := range []bool{false, true} {
			name := "topos/" + source + "/accepted"
			if failure {
				name = "topos/" + source + "/product rejects bearer"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "auth-token.json")
				authExpiry := time.Now().Add(time.Hour)
				if source == "expired login" {
					authExpiry = time.Now().Add(-time.Hour)
				}
				authBefore, _ := json.Marshal(map[string]any{"access_token": "auth-root", "refresh_token": "auth-refresh", "expires_at": authExpiry})
				if err := os.WriteFile(authPath, authBefore, 0600); err != nil {
					t.Fatal(err)
				}
				// Topos receives an actor token minted for its own audience,
				// never the root token on disk.
				wantBearer := "topos-actor"
				if source == "override" {
					wantBearer = "product-override"
				}
				var cellaMints, productCalls, toposMints, authRefreshes atomic.Int32
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
						if body.Audience == "toposd" {
							toposMints.Add(1)
							_, _ = w.Write([]byte(`{"actor_token":"topos-actor","expires_in":300}`))
							return
						}
						cellaMints.Add(1)
						_, _ = w.Write([]byte(`{"actor_token":"cella-actor","expires_in":300}`))
					case "/v1/agents":
						productCalls.Add(1)
						if got := r.Header.Get("Authorization"); got != "Bearer "+wantBearer {
							t.Errorf("product received %q, want its own bearer", got)
						}
						if failure {
							w.WriteHeader(http.StatusUnauthorized)
							_, _ = w.Write([]byte(`{"code":"product_rejected","message":"rejected product credential"}`))
						} else {
							_, _ = w.Write([]byte(`{"agents":[]}`))
						}
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				env := append(os.Environ(), "LATERE_CELLA_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_CELLA_URL="+server.URL+"/v1/environments", "TOPOS_API_URL="+server.URL, "TOPOS_TOKEN=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				if source == "override" {
					env = append(env, "TOPOS_TOKEN="+wantBearer)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "topos", "agents", "list")
				command.Env = env
				out, err := command.CombinedOutput()
				if failure {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "product_rejected") {
						t.Errorf("product rejection = %v: %s", err, out)
					}
				} else if err != nil {
					t.Errorf("product command = %v: %s", err, out)
				}
				// A Topos command mints for its own audience alone; Cella's
				// audience is never asked for on its behalf.
				if cellaMints.Load() != 0 || productCalls.Load() != 1 {
					t.Errorf("requests: Cella mints=%d product=%d, want 0/1", cellaMints.Load(), productCalls.Load())
				}
				wantToposMints := int32(1)
				if source == "override" {
					wantToposMints = 0
				}
				if toposMints.Load() != wantToposMints {
					t.Errorf("Topos actor mint calls = %d, want %d", toposMints.Load(), wantToposMints)
				}
				wantRefreshes := int32(0)
				if source == "expired login" {
					wantRefreshes = 1
				}
				if authRefreshes.Load() != wantRefreshes {
					t.Errorf("auth refresh calls = %d, want %d", authRefreshes.Load(), wantRefreshes)
				}
				if source == "expired login" {
					data, err := os.ReadFile(authPath)
					var saved map[string]any
					if err != nil || json.Unmarshal(data, &saved) != nil || saved["access_token"] != "renewed-root" || saved["refresh_token"] != "renewed-refresh" {
						t.Errorf("renewed login not saved: %v", err)
					}
				} else if data, err := os.ReadFile(authPath); err != nil || string(data) != string(authBefore) {
					t.Errorf("product command changed the saved login: %v", err)
				}
			})
		}
	}
}
