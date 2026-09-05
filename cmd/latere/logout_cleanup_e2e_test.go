// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogoutAttemptsBothLocalCredentialsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name                                    string
		blockCella, blockAuth, readOnly, absent bool
	}{
		{name: "success"},
		{name: "already logged out", absent: true},
		{name: "Cella directory", blockCella: true},
		{name: "auth directory", blockAuth: true},
		{name: "both directories", blockCella: true, blockAuth: true},
		{name: "Cella removal denied", blockCella: true, readOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			paths := []string{filepath.Join(root, "cella", "token.json"), filepath.Join(root, "auth", "auth-token.json")}
			blocked := []bool{tc.blockCella, tc.blockAuth}
			contents := []string{`{"access_token":"test-cella"}`, `{"access_token":"test-auth","refresh_token":"test-refresh"}`}
			for i, path := range paths {
				if tc.absent {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if blocked[i] && !tc.readOnly {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(path, "child")
				}
				if err := os.WriteFile(path, []byte(contents[i]), 0600); err != nil {
					t.Fatal(err)
				}
				if blocked[i] && tc.readOnly {
					makeTokenDirectoryReadOnly(t, filepath.Dir(path))
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodDelete && r.URL.Path == "/v1/tokens/current":
					if r.Header.Get("Authorization") != "Bearer test-cella" {
						t.Error("revocation used unexpected Cella credential")
					}
				case r.Method == http.MethodPost && r.URL.Path == "/revoke":
					if err := r.ParseForm(); err != nil || r.PostForm.Get("token") != "test-refresh" {
						t.Error("revocation used unexpected auth credential")
					}
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				// Even when remote revocation fails, remove every local token possible.
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "logout", "--api-url", server.URL, "--auth-url", server.URL)
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+paths[0], "LATERE_AUTH_TOKEN_FILE="+paths[1], "AUTH_URL="+server.URL, "SANDBOX_API_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			if tc.blockCella || tc.blockAuth {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
					t.Errorf("partial logout exit = %v: %s", err, out)
				}
				if strings.Contains(string(out), "Logged out.") {
					t.Errorf("partial logout reported success: %s", out)
				}
			} else if err != nil || !strings.Contains(string(out), "Logged out.") {
				t.Errorf("logout = %v: %s", err, out)
			}
			for i, path := range paths {
				if blocked[i] {
					if !strings.Contains(string(out), path) {
						t.Errorf("missing removal error for %s: %s", filepath.Base(path), out)
					}
					if !tc.readOnly {
						path = filepath.Join(path, "child")
					}
					if data, err := os.ReadFile(path); err != nil || string(data) != contents[i] {
						t.Errorf("failed cleanup changed blocked path: %v", err)
					}
				} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("logout left removable %s behind: %v", filepath.Base(path), err)
				}
			}
		})
	}
}
