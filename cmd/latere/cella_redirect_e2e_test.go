// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
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

func TestCellaRedirectsPreserveRequestMethodsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"get", "start", "mkdir", "delete", "export"} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(operation+"/"+strconv.Itoa(status), func(t *testing.T) {
				root := t.TempDir()
				tokenPath := filepath.Join(root, "token.json")
				if err := os.WriteFile(tokenPath, []byte(`{"access_token":"test-cella"}`), 0600); err != nil {
					t.Fatal(err)
				}
				args := []string{"cella", operation, "dev"}
				method, path, body := http.MethodGet, "/v1/sandboxes/dev", ""
				switch operation {
				case "start":
					method, path = http.MethodPost, path+"/start"
				case "mkdir":
					args = append(args, "/workspace/test")
					method, path, body = http.MethodPost, path+"/files/mkdir", `{"path":"/workspace/test"}`
				case "delete":
					method = http.MethodDelete
				case "export":
					method, path, body = http.MethodPost, path+"/files/export", `{}`
				}
				invalid := operation != "get" && status < 307
				var initial, redirected atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if r.URL.Path == "/redirected" {
						redirected.Add(1)
						if !invalid && (r.Method != method || string(data) != body) {
							t.Errorf("redirected request changed: %s %q, want %s %q", r.Method, data, method, body)
						}
						if operation == "export" {
							_, _ = w.Write([]byte("archive bytes"))
							return
						}
						_, _ = w.Write([]byte(`{"id":"test-id","name":"dev","state":"running"}`))
						return
					}
					initial.Add(1)
					if r.Method != method || r.URL.Path != path || string(data) != body || r.Header.Get("Authorization") != "Bearer test-cella" {
						t.Errorf("unexpected initial request: %s %s %q", r.Method, r.URL.Path, data)
					}
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(status)
				}))
				defer server.Close()
				args = append(args, "--api-url", server.URL)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				wantRedirected := int32(1)
				if invalid {
					wantRedirected = 0
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "redirect changed request method") {
						t.Errorf("method-changing redirect=%v; stderr=%q", err, stderr.String())
					}
					if stdout.Len() != 0 || strings.Contains(stderr.String(), "deleted dev") {
						t.Errorf("invalid redirect reported success: stdout=%q stderr=%q", stdout.String(), stderr.String())
					}
				} else if err != nil {
					t.Errorf("method-preserving redirect failed: %v; %s", err, stderr.String())
				} else if operation == "get" && !strings.Contains(stdout.String(), "test-id") || operation == "export" && stdout.String() != "archive bytes" {
					t.Errorf("missing redirected result: %q", stdout.String())
				}
				if initial.Load() != 1 || redirected.Load() != wantRedirected {
					t.Errorf("initial/redirected calls=%d/%d, want 1/%d", initial.Load(), redirected.Load(), wantRedirected)
				}
			})
		}
	}
}
