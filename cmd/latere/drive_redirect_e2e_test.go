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

func TestDriveRejectsMethodChangingRedirectsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"put", "mv", "rm", "get"} {
		for _, status := range []int{301, 302, 303} {
			t.Run(operation+"/"+strconv.Itoa(status), func(t *testing.T) {
				root := t.TempDir()
				src := filepath.Join(root, "source")
				if err := os.WriteFile(src, []byte("upload body"), 0600); err != nil {
					t.Fatal(err)
				}
				args, method := []string{"drive", operation}, http.MethodGet
				switch operation {
				case "put":
					args = append(args, src, "files/test")
					method = http.MethodPut
				case "mv":
					args = append(args, "files/test", "files/dest")
					method = http.MethodPost
				case "rm":
					args = append(args, "files/test")
					method = http.MethodDelete
				case "get":
					args = append(args, "files/test", "-o", "-")
				}
				var initial, redirected atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/redirected" {
						redirected.Add(1)
						if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
							t.Error("invalid redirected download")
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"path":"files/test","moved_from":"files/old","size":11}`))
						return
					}
					initial.Add(1)
					if r.Method != method || r.URL.Path != "/api/v1/files/me/files/test" || r.Header.Get("Authorization") != "Bearer test-drive" {
						t.Errorf("unexpected initial request: %s %s", r.Method, r.URL.Path)
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(status)
				}))
				defer server.Close()
				args = append(args, "--drive-url", server.URL, "--token", "test-drive")
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				var wantRedirected int32
				if operation == "get" {
					wantRedirected = 1
					if err != nil || !strings.Contains(stdout.String(), "files/test") {
						t.Errorf("download redirect failed: %v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
				} else {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "redirect changed request method") {
						t.Errorf("method-changing redirect = %v; stderr=%q", err, stderr.String())
					}
					if stdout.Len() != 0 || strings.Contains(stderr.String(), "Uploaded") || strings.Contains(stderr.String(), "Trashed") || strings.Contains(stderr.String(), "Moved") {
						t.Errorf("redirected mutation reported success: %q %q", stdout.String(), stderr.String())
					}
				}
				if initial.Load() != 1 || redirected.Load() != wantRedirected {
					t.Errorf("initial/redirected calls=%d/%d, want 1/%d", initial.Load(), redirected.Load(), wantRedirected)
				}
			})
		}
	}
}
