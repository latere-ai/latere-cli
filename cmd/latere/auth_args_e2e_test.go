// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
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
)

func TestAuthExtraArgumentsHaveNoEffectsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, verb := range []string{"login", "logout", "whoami", "print-token"} {
		for _, prefix := range []string{"", "auth"} {
			t.Run(prefix+"/"+verb, func(t *testing.T) {
				root := t.TempDir()
				tokenPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				before := map[string]string{
					tokenPath: `{"access_token":"old-cella"}`,
					authPath:  `{"access_token":"old-auth","refresh_token":"old-refresh"}`,
				}
				for path, contents := range before {
					if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/v1/sandboxes":
						_, _ = w.Write([]byte(`[]`))
					case "/tokeninfo":
						_, _ = w.Write([]byte(`{"sub":"test-user","principal_type":"user"}`))
					case "/v1/tokens/current", "/revoke":
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				args := []string{verb, "unexpected-arg"}
				if prefix != "" {
					args = append([]string{prefix}, args...)
				}
				if verb == "login" {
					args = append(args, "--token", "candidate", "--no-git")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+authPath,
					"AUTH_URL="+server.URL, "SANDBOX_API_URL="+server.URL, "XDG_CONFIG_HOME="+root,
					"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostics bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostics
				err := command.Run()
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostics.String(), "unexpected-arg") {
					t.Errorf("invalid command returned %v: %s", err, diagnostics.String())
				}
				if out.Len() != 0 {
					t.Error("invalid command emitted data to stdout")
				}
				if requests.Load() != 0 {
					t.Errorf("invalid command made %d requests", requests.Load())
				}
				for path, contents := range before {
					if got, err := os.ReadFile(path); err != nil || string(got) != contents {
						t.Errorf("invalid command changed %s: %v", filepath.Base(path), err)
					}
				}
			})
		}
	}
}
