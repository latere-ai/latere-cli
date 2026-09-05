// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshRejectsIncompleteTokenResponsesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, stage := range []string{"actor", "exchange"} {
		for _, state := range []string{"complete", "whitespace", "short content length", "interrupted chunks", "extra JSON", "trailing garbage", "301", "302", "303", "307", "308"} {
			t.Run(stage+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				cellaBefore, _ := json.Marshal(map[string]any{"access_token": "old-cella", "expires_at": time.Now().Add(-time.Hour)})
				authBefore := []byte(`{"access_token":"auth-root"}`)
				for path, data := range map[string][]byte{cellaPath: cellaBefore, authPath: authBefore} {
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				redirectStatus, _ := strconv.Atoi(state)
				invalid := state != "complete" && state != "whitespace" && redirectStatus != 307 && redirectStatus != 308
				var redirects atomic.Int32
				var originalBody atomic.Value
				var mints, exchanges, requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					var payload, current, bearer string
					redirected := strings.HasSuffix(r.URL.Path, "/redirected")
					if redirected {
						redirects.Add(1)
					}
					switch strings.TrimSuffix(r.URL.Path, "/redirected") {
					case "/actor-tokens":
						if !redirected {
							mints.Add(1)
						}
						current, bearer, payload = "actor", "auth-root", `{"actor_token":"new-actor"}`
					case "/v1/tokens/exchange":
						if !redirected {
							exchanges.Add(1)
						}
						current, bearer, payload = "exchange", "new-actor", `{"access_token":"new-cella"}`
					case "/v1/sandboxes":
						requests.Add(1)
						want := "Bearer new-cella"
						if invalid {
							want = "Bearer old-cella"
						}
						if got := r.Header.Get("Authorization"); got != want {
							t.Errorf("API bearer = %q, want %q", got, want)
						}
						if r.Header.Get("Authorization") == "Bearer old-cella" {
							w.WriteHeader(http.StatusUnauthorized)
							_, _ = w.Write([]byte(`{"code":"expired","message":"original token expired"}`))
						} else {
							_, _ = w.Write([]byte(`[]`))
						}
						return
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+bearer {
						t.Errorf("invalid %s request", current)
					}
					if current == stage && redirectStatus != 0 {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						if !redirected {
							originalBody.Store(string(body))
							w.Header().Set("Location", r.URL.Path+"/redirected")
							w.WriteHeader(redirectStatus)
							return
						}
						if string(body) != originalBody.Load() || len(body) == 0 || r.Header.Get("Content-Type") != "application/json" {
							t.Error("redirect lost token request body or content type")
						}
					}
					if current == stage {
						switch state {
						case "whitespace":
							payload += " \r\n\t"
						case "short content length":
							w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
						case "extra JSON":
							payload += " {}"
						case "trailing garbage":
							payload += " garbage"
						}
					}
					_, _ = w.Write([]byte(payload))
					if current == stage && state == "interrupted chunks" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "cella", "list", "--api-url", server.URL)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				out, err := command.CombinedOutput()
				if invalid {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !bytes.Contains(out, []byte("expired")) {
						t.Errorf("invalid token response exit = %v: %s", err, out)
					}
				} else if err != nil {
					t.Errorf("complete token response failed: %v: %s", err, out)
				}
				for path, before := range map[string][]byte{cellaPath: cellaBefore, authPath: authBefore} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if path == cellaPath && !invalid {
						var saved struct {
							AccessToken string `json:"access_token"`
						}
						if json.Unmarshal(data, &saved) != nil || saved.AccessToken != "new-cella" {
							t.Error("complete exchange did not save the replacement credential")
						}
					} else if !bytes.Equal(data, before) {
						t.Errorf("unexpected credential change in %s", filepath.Base(path))
					}
				}
				wantRedirects := int32(0)
				if redirectStatus == 307 || redirectStatus == 308 {
					wantRedirects = 1
				}
				if redirects.Load() != wantRedirects {
					t.Errorf("redirect requests = %d, want %d", redirects.Load(), wantRedirects)
				}
				wantExchanges := int32(1)
				if stage == "actor" && invalid {
					wantExchanges = 0
				}
				if mints.Load() != 1 || exchanges.Load() != wantExchanges || requests.Load() != 1 {
					t.Errorf("mint/exchange/API calls = %d/%d/%d; want 1/%d/1", mints.Load(), exchanges.Load(), requests.Load(), wantExchanges)
				}
			})
		}
	}
}
