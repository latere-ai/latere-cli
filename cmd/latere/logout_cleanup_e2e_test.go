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
		name                      string
		blocked, readOnly, absent bool
	}{
		{name: "success"},
		{name: "already logged out", absent: true},
		{name: "login directory", blocked: true},
		{name: "removal denied", blocked: true, readOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			authPath := filepath.Join(root, "auth", "auth-token.json")
			const contents = `{"access_token":"test-auth","refresh_token":"test-refresh"}`
			if !tc.absent {
				if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
					t.Fatal(err)
				}
				path := authPath
				if tc.blocked && !tc.readOnly {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(path, "child")
				}
				if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
				if tc.blocked && tc.readOnly {
					makeTokenDirectoryReadOnly(t, filepath.Dir(path))
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/revoke" {
					t.Errorf("unexpected request: %s %s; logout speaks to the issuer alone", r.Method, r.URL.Path)
				} else if err := r.ParseForm(); err != nil || r.PostForm.Get("token") != "test-refresh" {
					t.Error("revocation used an unexpected credential")
				}
				// Even when remote revocation fails, remove the local login.
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "logout", "--auth-url", server.URL)
			command.Env = append(os.Environ(), "LATERE_CELLA_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			if tc.blocked {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
					t.Errorf("blocked logout exit = %v: %s", err, out)
				}
				if strings.Contains(string(out), "Logged out.") {
					t.Errorf("blocked logout reported success: %s", out)
				}
				if !strings.Contains(string(out), authPath) {
					t.Errorf("missing removal error for %s: %s", filepath.Base(authPath), out)
				}
				path := authPath
				if !tc.readOnly {
					path = filepath.Join(path, "child")
				}
				if data, err := os.ReadFile(path); err != nil || string(data) != contents {
					t.Errorf("failed cleanup changed blocked path: %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(string(out), "Logged out.") {
				t.Errorf("logout = %v: %s", err, out)
			}
			if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("logout left removable %s behind: %v", filepath.Base(authPath), err)
			}
		})
	}
}
