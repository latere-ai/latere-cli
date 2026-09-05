// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestOrgSwitchUpdatesCellaIdentityE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, org                 string
		failExchange, failRefresh bool
	}{
		{"organization", "new-org", false, false},
		{"personal", "", false, false},
		{"exchange failure", "new-org", true, false},
		{"refresh failure", "new-org", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tokenPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
			oldAuth := api.Token{AccessToken: "old-auth", RefreshToken: "old-refresh"}
			if err := api.SaveToken(tokenPath, api.Token{AccessToken: "old-cella"}); err != nil {
				t.Fatal(err)
			}
			if err := api.SaveToken(authPath, oldAuth); err != nil {
				t.Fatal(err)
			}
			claims, _ := json.Marshal(map[string]string{"org_id": tc.org, "sub": "test-user"})
			newAuth := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".test-signature"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if !r.Form.Has("org_id") || r.Form.Get("org_id") != tc.org || r.Form.Get("refresh_token") != "old-refresh" {
						t.Error("wrong refresh context")
					}
					if tc.failRefresh {
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"error":"access_denied"}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": newAuth, "refresh_token": "new-refresh", "expires_in": 3600})
				case "/actor-tokens":
					if r.Header.Get("Authorization") != "Bearer "+newAuth {
						t.Error("actor token minted from previous identity")
					}
					_, _ = w.Write([]byte(`{"actor_token":"new-actor"}`))
				case "/v1/tokens/exchange":
					if r.Header.Get("Authorization") != "Bearer new-actor" {
						t.Error("Cella exchange used previous identity")
					}
					if tc.failExchange {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(`{"code":"unavailable"}`))
						return
					}
					_, _ = w.Write([]byte(`{"access_token":"new-cella"}`))
				case "/v1/sandboxes":
					if r.Header.Get("Authorization") != "Bearer new-cella" {
						t.Error("Cella command still uses the previous organization")
					}
					_, _ = w.Write([]byte(`[]`))
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			env := append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "SANDBOX_API_URL="+server.URL, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			run := func(args ...string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = env
				return command.CombinedOutput()
			}
			args := []string{"org", "--auth-url", server.URL}
			if tc.org == "" {
				args = append(args, "--personal")
			} else {
				args = append(args, tc.org)
			}
			out, err := run(args...)
			if tc.failExchange || tc.failRefresh {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
					t.Errorf("switch error = %v; output: %s", err, out)
				}
			} else if err != nil {
				t.Fatalf("org switch: %v\n%s", err, out)
			}
			gotAuth, err := api.LoadToken(authPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.failRefresh {
				if gotAuth != oldAuth {
					t.Error("rejected refresh changed auth token")
				}
				got, err := api.LoadToken(tokenPath)
				if err != nil || got.AccessToken != "old-cella" {
					t.Errorf("rejected refresh changed Cella token: %v", err)
				}
				return
			}
			if gotAuth.AccessToken != newAuth || gotAuth.RefreshToken != "new-refresh" {
				t.Error("new root credential was not retained")
			}
			if tc.failExchange {
				if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
					t.Error("failed exchange retained the previous organization's Cella token")
				}
				return
			}
			got, err := api.LoadToken(tokenPath)
			if err != nil || got.AccessToken != "new-cella" {
				t.Errorf("Cella credential not updated: %v", err)
			}
			out, err = run("org")
			want := tc.org
			if want == "" {
				want = "personal"
			}
			if err != nil || strings.TrimSpace(string(out)) != want {
				t.Errorf("shown context = %s (%v), want %s", out, err, want)
			}
			if out, err := run("cella", "list"); err != nil {
				t.Fatalf("Cella list: %v\n%s", err, out)
			}
		})
	}
}
