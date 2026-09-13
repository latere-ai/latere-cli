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
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestPastedLoginPreservesSessionUntilVerifiedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, input := range []string{"flag", "stdin"} {
		for _, tc := range []struct {
			name          string
			status        int
			blockSave     bool
			contextArgs   []string
			rejectContext bool
		}{
			{name: "rejected", status: http.StatusUnauthorized},
			{name: "unavailable", status: http.StatusServiceUnavailable},
			{name: "success", status: http.StatusOK},
			{name: "save_failed", status: http.StatusOK, blockSave: true},
			{name: "personal context", status: http.StatusOK, contextArgs: []string{"--personal"}, rejectContext: true},
			{name: "organization context", status: http.StatusOK, contextArgs: []string{"--org-id", "new-org"}, rejectContext: true},
			{name: "false personal flag", status: http.StatusOK, contextArgs: []string{"--personal=false"}},
			{name: "empty organization", status: http.StatusOK, contextArgs: []string{"--org-id", ""}},
		} {
			t.Run(input+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				authDir := filepath.Join(root, "auth")
				if err := os.Mkdir(authDir, 0700); err != nil {
					t.Fatal(err)
				}
				authPath := filepath.Join(authDir, "auth-token.json")
				const authBefore = `{"access_token":"old-auth"}`
				if err := os.WriteFile(authPath, []byte(authBefore), 0600); err != nil {
					t.Fatal(err)
				}
				if tc.blockSave {
					makeTokenDirectoryReadOnly(t, authDir)
				}
				assertUnchanged := func() {
					data, err := os.ReadFile(authPath)
					if err != nil || string(data) != authBefore {
						t.Errorf("the saved login changed before a verified login: %v", err)
					}
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.URL.Path != "/tokeninfo" || r.Header.Get("Authorization") != "Bearer candidate-token" {
						t.Error("verification did not present the submitted token to the issuer")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assertUnchanged()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					if tc.status == http.StatusOK {
						_, _ = w.Write([]byte(`{"sub":"u-1"}`))
					} else {
						_, _ = w.Write([]byte(`{"code":"verification_failed"}`))
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				args := []string{"login", "--no-git", "--auth-url", server.URL}
				if input == "flag" {
					args = append(args, "--token", "candidate-token")
				}
				args = append(args, tc.contextArgs...)
				command := exec.CommandContext(ctx, binary, args...)
				if input == "stdin" {
					command.Stdin = strings.NewReader("candidate-token\n")
				}
				command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				out, err := command.CombinedOutput()
				if tc.rejectContext {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "pasted token") {
						t.Errorf("incompatible token context returned %v: %s", err, out)
					}
					if requests.Load() != 0 {
						t.Errorf("incompatible token context made %d requests", requests.Load())
					}
					assertUnchanged()
					return
				}
				if tc.status != http.StatusOK || tc.blockSave {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("failed login exit = %v; output: %s", err, out)
					}
					if strings.Contains(string(out), "Logged in.") {
						t.Errorf("failed login reported success: %s", out)
					}
					assertUnchanged()
					return
				}
				if err != nil {
					t.Fatalf("login: %v\n%s", err, out)
				}
				t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
				got, err := api.LoadAuthToken()
				if err != nil || got.AccessToken != "candidate-token" {
					t.Errorf("verified token was not saved: %v", err)
				}
				if got.RefreshToken != "" {
					t.Error("a pasted login kept a refresh grant of the previous identity")
				}
			})
		}
	}
}

// Verify permissions are enforced before using them to inject a storage failure.
func makeTokenDirectoryReadOnly(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	probe := filepath.Join(dir, "permission-probe")
	if err := os.WriteFile(probe, nil, 0600); err == nil {
		t.Skip("filesystem or user does not enforce directory write permissions")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
}
