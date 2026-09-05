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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthFormsRequireCompleteRequestsAndResponsesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"org", "logout"} {
		for _, tc := range []struct {
			name             string
			redirect, status int
			broken, chunked  bool
		}{
			{name: "301", redirect: 301},
			{name: "302", redirect: 302},
			{name: "303", redirect: 303},
			{name: "307", redirect: 307},
			{name: "308", redirect: 308},
			{name: "complete"},
			{name: "whitespace"},
			{name: "short content length", broken: true},
			{name: "interrupted chunks", broken: true, chunked: true},
			{name: "incomplete error", status: 503, broken: true},
			{name: "no content", status: 204},
		} {
			if operation == "org" && tc.status == 204 {
				continue // A successful token response must contain an access token.
			}
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				before := map[string]string{
					cellaPath: `{"access_token":"old-cella"}`,
					authPath:  `{"access_token":"old-auth","refresh_token":"old-refresh","client_id":"test-client"}`,
				}
				for path, contents := range before {
					if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
				}
				invalidRedirect := tc.redirect != 0 && tc.redirect < 307
				invalid := invalidRedirect || tc.broken || tc.status >= 400
				wantError := ""
				switch {
				case invalidRedirect:
					wantError = "redirect changed request method"
				case tc.status >= 400:
					wantError = strconv.Itoa(tc.status)
				case tc.broken:
					wantError = "unexpected EOF"
				}
				var initial, redirected, exchanges atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/actor-tokens":
						exchanges.Add(1)
						_, _ = w.Write([]byte(`{"actor_token":"new-actor"}`))
						return
					case "/v1/tokens/exchange":
						exchanges.Add(1)
						_, _ = w.Write([]byte(`{"access_token":"new-cella"}`))
						return
					case "/v1/tokens/current":
						if operation != "logout" || r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer old-cella" {
							t.Error("unexpected Cella revocation")
						}
						w.WriteHeader(http.StatusNoContent)
						return
					case "/token", "/revoke":
						initial.Add(1)
					case "/redirected":
						redirected.Add(1)
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.PostForm.Get("client_id") != "test-client" {
						t.Error("auth redirect lost method, content type, or client ID")
					}
					if operation == "org" {
						if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "old-refresh" || r.PostForm.Get("org_id") != "new-org" {
							t.Error("org request lost refresh token or requested organization")
						}
					} else if r.PostForm.Get("token") != "old-refresh" || r.PostForm.Get("token_type_hint") != "refresh_token" {
						t.Error("revocation request lost refresh token")
					}
					if tc.redirect != 0 && r.URL.Path != "/redirected" {
						w.Header().Set("Location", "/redirected")
						w.WriteHeader(tc.redirect)
						return
					}
					payload := `{"access_token":"new-auth","refresh_token":"new-refresh","expires_in":3600}`
					if tc.name == "whitespace" {
						payload += " \r\n\t"
					}
					if tc.broken && !tc.chunked {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						if tc.status == 204 {
							return
						}
					}
					_, _ = w.Write([]byte(payload))
					if tc.chunked {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				}))
				defer server.Close()
				args := []string{"org", "new-org", "--auth-url", server.URL}
				if operation == "logout" {
					args = []string{"logout", "--auth-url", server.URL, "--api-url", server.URL}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "SANDBOX_API_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				out, err := command.CombinedOutput()
				if operation == "org" && invalid {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || strings.Contains(string(out), "Switched to") {
						t.Errorf("invalid org response reported success: %v: %s", err, out)
					}
				} else if err != nil {
					t.Errorf("command failed: %v: %s", err, out)
				}
				if wantError != "" && !strings.Contains(string(out), wantError) {
					t.Errorf("missing failure diagnostic: %s", out)
				}
				if operation == "logout" && (!strings.Contains(string(out), "Logged out.") || invalid != strings.Contains(string(out), "warning:")) {
					t.Errorf("logout revocation warning or completion missing: %s", out)
				}
				for path, contents := range before {
					data, err := os.ReadFile(path)
					if operation == "logout" {
						if !errors.Is(err, os.ErrNotExist) {
							t.Errorf("logout retained credential: %s", filepath.Base(path))
						}
					} else if invalid {
						if err != nil || string(data) != contents {
							t.Errorf("rejected org response changed credential: %s", filepath.Base(path))
						}
					} else {
						var saved struct {
							AccessToken string `json:"access_token"`
						}
						want := "new-auth"
						if path == cellaPath {
							want = "new-cella"
						}
						if err != nil || json.Unmarshal(data, &saved) != nil || saved.AccessToken != want {
							t.Errorf("valid org response did not update credential: %s", filepath.Base(path))
						}
					}
				}
				wantRedirects, wantExchanges := int32(0), int32(0)
				if !invalid {
					if tc.redirect != 0 {
						wantRedirects = 1
					}
					if operation == "org" {
						wantExchanges = 2
					}
				}
				if initial.Load() != 1 || redirected.Load() != wantRedirects || exchanges.Load() != wantExchanges {
					t.Errorf("initial/redirect/exchange calls = %d/%d/%d, want 1/%d/%d", initial.Load(), redirected.Load(), exchanges.Load(), wantRedirects, wantExchanges)
				}
			})
		}
	}
}
