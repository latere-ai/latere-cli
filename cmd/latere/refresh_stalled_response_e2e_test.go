// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func TestRefreshRetriesWithoutDrainingStalledResponseE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"list", "mkdir"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
			authBefore := []byte(`{"access_token":"auth-root"}`)
			for path, data := range map[string][]byte{cellaPath: []byte(`{"access_token":"old-cella"}`), authPath: authBefore} {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var requests, mints, exchanges atomic.Int32
			stopped := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/actor-tokens":
					mints.Add(1)
					_, _ = w.Write([]byte(`{"actor_token":"new-actor"}`))
				case "/v1/tokens/exchange":
					exchanges.Add(1)
					_, _ = w.Write([]byte(`{"access_token":"new-cella"}`))
				case "/v1/sandboxes", "/v1/sandboxes/dev/files/mkdir":
					requests.Add(1)
					if operation == "mkdir" {
						body, err := io.ReadAll(r.Body)
						if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/dev/files/mkdir" || err != nil || string(body) != `{"path":"/workspace/test"}` {
							t.Errorf("mkdir request was not replayed intact: %s %s %q (%v)", r.Method, r.URL.Path, body, err)
						}
					} else if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes" {
						t.Errorf("unexpected list request: %s %s", r.Method, r.URL.Path)
					}
					switch r.Header.Get("Authorization") {
					case "Bearer old-cella":
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"code":"expired","message":"expired token"}`))
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						stopped <- struct{}{}
					case "Bearer new-cella":
						if operation == "mkdir" {
							w.WriteHeader(http.StatusNoContent)
						} else {
							_, _ = w.Write([]byte(`[]`))
						}
					default:
						t.Error("unexpected API bearer")
						w.WriteHeader(http.StatusUnauthorized)
					}
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			args := []string{"cella", operation, "--api-url", server.URL}
			if operation == "mkdir" {
				args = append(args, "dev", "/workspace/test")
			}
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("stalled 401 prevented successful retry: %v; %s", err, out)
			} else if operation == "list" && !strings.Contains(string(out), "No cellas") {
				t.Errorf("missing successful list result: %s", out)
			}
			if requests.Load() != 2 || mints.Load() != 1 || exchanges.Load() != 1 {
				t.Errorf("API/mint/exchange calls = %d/%d/%d, want 2/1/1", requests.Load(), mints.Load(), exchanges.Load())
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Error("retry left the original response request open")
				server.CloseClientConnections()
			}
			data, err := os.ReadFile(cellaPath)
			var saved struct {
				AccessToken string `json:"access_token"`
			}
			if err != nil || json.Unmarshal(data, &saved) != nil || saved.AccessToken != "new-cella" {
				t.Errorf("refreshed credential not persisted: %v", err)
			}
			if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, authBefore) {
				t.Errorf("refresh unexpectedly changed auth root: %v", err)
			}
		})
	}
}
