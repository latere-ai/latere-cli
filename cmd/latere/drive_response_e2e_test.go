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

func TestDriveCommandsRequireCompleteResponsesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"ls", "put", "rm"} {
		for _, state := range []string{"complete", "whitespace", "short content length", "interrupted chunks", "extra JSON", "trailing garbage", "no content"} {
			if state == "no content" && operation != "rm" {
				continue
			}
			t.Run(operation+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				src := filepath.Join(root, "source")
				if err := os.WriteFile(src, []byte("test upload"), 0600); err != nil {
					t.Fatal(err)
				}
				payload, method, path := `{"entries":[{"path":"files/test"}]}`, http.MethodGet, "/api/v1/files/me/files"
				args := []string{"drive", operation}
				switch operation {
				case "ls":
					args = append(args, "--json")
				case "put":
					args = append(args, src, "files/test")
					payload, method, path = `{"path":"files/test","size":11}`, http.MethodPut, "/api/v1/files/me/files/test"
				case "rm":
					args = append(args, "files/test")
					payload, method, path = `{}`, http.MethodDelete, "/api/v1/files/me/files/test"
				}
				wantError := ""
				switch state {
				case "whitespace":
					payload += " \r\n\t"
				case "extra JSON":
					payload += " {}"
					if operation != "rm" {
						wantError = "multiple JSON values"
					}
				case "trailing garbage":
					payload += " garbage"
					if operation != "rm" {
						wantError = "invalid character"
					}
				case "short content length", "interrupted chunks":
					wantError = "unexpected EOF"
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != method || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer test-drive" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					if operation == "put" {
						body, err := io.ReadAll(r.Body)
						if err != nil || string(body) != "test upload" {
							t.Errorf("upload body = %q (%v)", body, err)
						}
					}
					if state == "no content" {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if state == "short content length" {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					_, _ = w.Write([]byte(payload))
					if state == "interrupted chunks" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
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
				if wantError != "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), wantError) {
						t.Errorf("invalid response = %v, stderr=%q; want %q", err, stderr.String(), wantError)
					}
					if stdout.Len() != 0 || strings.Contains(stderr.String(), "Uploaded") || strings.Contains(stderr.String(), "Trashed") {
						t.Errorf("invalid response reported success: stdout=%q stderr=%q", stdout.String(), stderr.String())
					}
				} else if err != nil {
					t.Errorf("complete response failed: %v; %s", err, stderr.String())
				} else if !strings.Contains(stdout.String()+stderr.String(), "files/test") {
					t.Errorf("missing result: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				if calls.Load() != 1 {
					t.Errorf("request count=%d, want one", calls.Load())
				}
			})
		}
	}
}
