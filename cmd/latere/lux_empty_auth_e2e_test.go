// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
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

func TestLuxRejectsEmptySavedAuthE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"raw", "exports", "alias", "actor"} {
		for _, state := range []string{"empty object", "null", "valid", "refreshable"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "auth-token.json")
				data := []byte(`{}`)
				switch state {
				case "null":
					data = []byte(`null`)
				case "valid":
					data = []byte(`{"access_token":"valid-root"}`)
				case "refreshable":
					data, _ = json.Marshal(map[string]any{"refresh_token": "valid-refresh", "expires_at": time.Now().Add(-time.Hour)})
				}
				if err := os.WriteFile(authPath, data, 0600); err != nil {
					t.Fatal(err)
				}
				invalid := state == "empty object" || state == "null"
				var refreshes, mints atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/token":
						refreshes.Add(1)
						_, _ = w.Write([]byte(`{"access_token":"valid-root","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
					case "/actor-tokens":
						mints.Add(1)
						if r.Header.Get("Authorization") != "Bearer valid-root" {
							t.Error("attempted actor mint without a usable auth credential")
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						_, _ = w.Write([]byte(`{"actor_token":"valid-actor"}`))
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				args := []string{"lux", "env", "--raw"}
				switch mode {
				case "exports":
					args = []string{"lux", "env", "--compat", "openai"}
				case "alias":
					args = []string{"lux", "token"}
				case "actor":
					args = append(args, "--ttl", "1m")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LUX_API_URL="+server.URL, "LATERE_LUX_TOKEN=", "AUTH_CLIENT_ID=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if invalid {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "latere login") {
						t.Errorf("empty credential = %v; stderr: %s", err, stderr.String())
					}
					if stdout.Len() != 0 {
						t.Errorf("empty credential produced shell output: %q", stdout.String())
					}
				} else {
					want := "valid-root"
					if mode == "actor" {
						want = "valid-actor"
					}
					if err != nil || !strings.Contains(stdout.String(), want) {
						t.Errorf("valid credential = %v; stdout: %q; stderr: %s", err, stdout.String(), stderr.String())
					}
				}
				var wantRefresh, wantMint int32
				if state == "refreshable" {
					wantRefresh = 1
				}
				if mode == "actor" && !invalid {
					wantMint = 1
				}
				if refreshes.Load() != wantRefresh || mints.Load() != wantMint {
					t.Errorf("refresh/mint calls = %d/%d, want %d/%d", refreshes.Load(), mints.Load(), wantRefresh, wantMint)
				}
				if state != "refreshable" {
					if saved, err := os.ReadFile(authPath); err != nil || !bytes.Equal(saved, data) {
						t.Errorf("credential validation modified auth file: %v", err)
					}
				}
			})
		}
	}
}
