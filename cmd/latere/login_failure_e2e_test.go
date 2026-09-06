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
			name           string
			status         int
			blockAuthClear bool
			contextArgs    []string
			rejectContext  bool
		}{
			{name: "rejected", status: http.StatusUnauthorized},
			{name: "unavailable", status: http.StatusServiceUnavailable},
			{name: "success", status: http.StatusOK},
			{name: "auth_cleanup_failed", status: http.StatusOK, blockAuthClear: true},
			{name: "personal context", status: http.StatusOK, contextArgs: []string{"--personal"}, rejectContext: true},
			{name: "organization context", status: http.StatusOK, contextArgs: []string{"--org-id", "new-org"}, rejectContext: true},
			{name: "false personal flag", status: http.StatusOK, contextArgs: []string{"--personal=false"}},
			{name: "empty organization", status: http.StatusOK, contextArgs: []string{"--org-id", ""}},
		} {
			t.Run(input+"/"+tc.name, func(t *testing.T) {
				status := tc.status
				root := t.TempDir()
				authDir := filepath.Join(root, "auth")
				if err := os.Mkdir(authDir, 0700); err != nil {
					t.Fatal(err)
				}
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(authDir, "auth-token.json")
				cellaBefore, authBefore := `{"access_token":"old-cella"}`, `{"access_token":"old-auth"}`
				for path, contents := range map[string]string{cellaPath: cellaBefore, authPath: authBefore} {
					if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if tc.blockAuthClear {
					makeTokenDirectoryReadOnly(t, authDir)
				}
				assertUnchanged := func() {
					for path, before := range map[string]string{cellaPath: cellaBefore, authPath: authBefore} {
						data, err := os.ReadFile(path)
						if err != nil || string(data) != before {
							t.Errorf("saved %s changed before a verified login: %v", filepath.Base(path), err)
						}
					}
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes" || r.Header.Get("Authorization") != "Bearer candidate-token" {
						t.Error("verification did not use the submitted token")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assertUnchanged()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(`[]`))
					} else {
						_, _ = w.Write([]byte(`{"code":"verification_failed"}`))
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				args := []string{"login", "--no-git", "--api-url", server.URL}
				if input == "flag" {
					args = append(args, "--token", "candidate-token")
				}
				args = append(args, tc.contextArgs...)
				command := exec.CommandContext(ctx, binary, args...)
				if input == "stdin" {
					command.Stdin = strings.NewReader("candidate-token\n")
				}
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
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
				if tc.blockAuthClear {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("auth cleanup failure exit = %v: %s", err, out)
					}
					if strings.Contains(string(out), "Logged in.") {
						t.Errorf("failed login reported success: %s", out)
					}
					if _, err := os.Stat(cellaPath); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("auth cleanup failure retained the new Cella token: %v", err)
					}
					data, err := os.ReadFile(authPath)
					if err != nil || string(data) != authBefore {
						t.Errorf("failed auth cleanup changed the previous root credential: %v", err)
					}
					return
				}
				if status != http.StatusOK {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("failed verification exit = %v; output: %s", err, out)
					}
					assertUnchanged()
					return
				}
				if err != nil {
					t.Fatalf("login: %v\n%s", err, out)
				}
				got, err := api.LoadToken(cellaPath)
				if err != nil || got.AccessToken != "candidate-token" {
					t.Errorf("verified token was not saved: %v", err)
				}
				if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("successful paste login retained an unrelated auth identity: %v", err)
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
