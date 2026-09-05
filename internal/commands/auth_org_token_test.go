// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestReplaceCellaOrgToken(t *testing.T) {
	for _, mode := range []string{"success", "exchange failure", "remove failure", "save failure"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token.json")
			t.Setenv("LATERE_TOKEN_FILE", path)
			// The explicit auth endpoint must win over ambient configuration.
			t.Setenv("AUTH_URL", "http://127.0.0.1:1")
			if mode == "remove failure" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "child"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := api.SaveToken("", api.Token{AccessToken: "old-cella"}); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Error("old Cella token was not removed before exchange")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/actor-tokens":
					if r.Header.Get("Authorization") != "Bearer new-root" {
						t.Error("actor mint used wrong auth token")
					}
					_, _ = w.Write([]byte(`{"actor_token":"new-actor"}`))
				case "/v1/tokens/exchange":
					if mode == "exchange failure" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if mode == "save failure" {
						if err := os.Mkdir(path, 0700); err != nil {
							t.Error(err)
						}
					}
					_, _ = w.Write([]byte(`{"access_token":"new-cella"}`))
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			t.Setenv("SANDBOX_API_URL", server.URL)
			err := replaceCellaOrgToken(t.Context(), server.URL, "new-root")
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				got, err := api.LoadToken("")
				if err != nil || got.AccessToken != "new-cella" {
					t.Fatalf("replacement not saved: %v", err)
				}
			} else if err == nil {
				t.Fatal("failed replacement reported success")
			}
			if mode == "remove failure" && requests.Load() != 0 {
				t.Error("exchange started after failing to remove stale token")
			}
			if mode == "exchange failure" {
				if _, err := api.LoadToken(""); !errors.Is(err, api.ErrNoToken) {
					t.Errorf("exchange failure left a stale token: %v", err)
				}
			}
		})
	}
}
