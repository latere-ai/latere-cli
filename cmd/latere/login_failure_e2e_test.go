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
		for _, status := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable, http.StatusOK} {
			t.Run(input+"/"+http.StatusText(status), func(t *testing.T) {
				root := t.TempDir()
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				cellaBefore, authBefore := `{"access_token":"old-cella"}`, `{"access_token":"old-auth"}`
				for path, contents := range map[string]string{cellaPath: cellaBefore, authPath: authBefore} {
					if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
				}
				assertUnchanged := func() {
					for path, before := range map[string]string{cellaPath: cellaBefore, authPath: authBefore} {
						data, err := os.ReadFile(path)
						if err != nil || string(data) != before {
							t.Errorf("saved %s changed before a verified login: %v", filepath.Base(path), err)
						}
					}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				command := exec.CommandContext(ctx, binary, args...)
				if input == "stdin" {
					command.Stdin = strings.NewReader("candidate-token\n")
				}
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				out, err := command.CombinedOutput()
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
