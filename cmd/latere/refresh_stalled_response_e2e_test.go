// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"fmt"
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
			authPath := filepath.Join(root, "auth-token.json")
			authBefore := []byte(`{"access_token":"auth-root"}`)
			if err := os.WriteFile(authPath, authBefore, 0600); err != nil {
				t.Fatal(err)
			}
			var requests, mints atomic.Int32
			stopped := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/actor-tokens":
					n := mints.Add(1)
					_, _ = fmt.Fprintf(w, `{"actor_token":"actor-%d","expires_in":300}`, n)
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
					case "Bearer actor-1":
						// The first actor token has lapsed at Cella. The
						// response stalls after the status, so a retry that
						// drained it would hang.
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"code":"expired","message":"expired token"}`))
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						stopped <- struct{}{}
					case "Bearer actor-2":
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
			command.Env = append(os.Environ(), "LATERE_CELLA_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("stalled 401 prevented successful retry: %v; %s", err, out)
			} else if operation == "list" && !strings.Contains(string(out), "No cellas") {
				t.Errorf("missing successful list result: %s", out)
			}
			if requests.Load() != 2 || mints.Load() != 2 {
				t.Errorf("API/mint calls = %d/%d, want 2/2", requests.Load(), mints.Load())
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Error("retry left the original response request open")
				server.CloseClientConnections()
			}
			// A re-minted product token lives in memory alone; the login on
			// disk is untouched.
			if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, authBefore) {
				t.Errorf("the re-mint changed the saved login: %v", err)
			}
		})
	}
}
