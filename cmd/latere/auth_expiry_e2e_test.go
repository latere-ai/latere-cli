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
			if product == "cella" && state != "expired" {
				continue // Cella's ordinary refresh is covered by the session e2e.
			}
			t.Run(product+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				var expiry time.Time
				switch state {
				case "expired":
					expiry = time.Now().Add(-time.Hour)
				case "near expiry":
					expiry = time.Now().Add(45 * time.Second)
				}
				if err := api.SaveToken(cellaPath, api.Token{AccessToken: "saved-cella"}); err != nil {
					t.Fatal(err)
				}
				if err := api.SaveToken(authPath, api.Token{AccessToken: "saved-auth", ExpiresAt: expiry}); err != nil {
					t.Fatal(err)
				}
				before := map[string][]byte{}
				for _, path := range []string{cellaPath, authPath} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					before[path] = data
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if product == "cella" {
						if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes" || r.Header.Get("Authorization") != "Bearer saved-cella" {
							t.Error("Cella tried to refresh using the expired auth root")
						}
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"code":"expired_cella"}`))
						return
					}
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer saved-auth" {
						t.Error("unexpected product request or credential")
					}
					w.Header().Set("Content-Type", "application/json")
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
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Stdin = strings.NewReader("protocol=https\nhost=drive.latere.ai\n\n")
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "DRIVE_API_URL="+server.URL, "TOPOS_API_URL="+server.URL, "LUX_API_URL="+server.URL, "DRIVE_HOST=drive.latere.ai", "LATERE_DRIVE_TOKEN=", "LATERE_LUX_TOKEN=", "TOPOS_TOKEN=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if state == "expired" {
					wantError := "latere login"
					var wantRequests int32
					if product == "cella" {
						wantError = "expired_cella"
						wantRequests = 1 // The Cella bearer is checked before root refresh.
					}
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
					wantRequests := int32(1)
					if product == "lux export" || product == "git helper" {
						wantRequests = 0
						if !strings.Contains(stdout.String(), "saved-auth") {
							t.Errorf("missing usable credential: %q", stdout.String())
						}
					}
					if requests.Load() != wantRequests {
						t.Errorf("product requests = %d, want %d", requests.Load(), wantRequests)
					}
				}
				for path, contents := range before {
					if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, contents) {
						t.Errorf("credential resolution changed %s: %v", filepath.Base(path), err)
					}
				}
			})
		}
	}
}
