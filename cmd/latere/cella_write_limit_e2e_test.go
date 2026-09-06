// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCellaWriteSizeLimitE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	token := filepath.Join(root, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	const limit = 10 << 20
	for _, mode := range []string{"file", "stdin", "open stdin"} {
		for _, size := range []int{0, 7, limit, limit + 1} {
			if mode == "open stdin" && size <= limit {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				contents := bytes.Repeat([]byte{'a'}, size)
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPut || r.URL.Path != "/v1/sandboxes/dev/files" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					var body struct{ Path, Content string }
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					data, err := base64.StdEncoding.DecodeString(body.Content)
					if err != nil || !bytes.Equal(data, contents) || body.Path != "/workspace/file" {
						t.Errorf("write changed data: bytes=%d path=%q err=%v", len(data), body.Path, err)
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				defer server.Close()
				args := []string{"cella", "write", "dev", "/workspace/file", "--api-url", server.URL}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				var wrote chan error
				switch mode {
				case "file":
					input := filepath.Join(t.TempDir(), "input")
					if err := os.WriteFile(input, contents, 0600); err != nil {
						t.Fatal(err)
					}
					command.Args = append(command.Args, "--input", input)
				case "stdin":
					command.Stdin = bytes.NewReader(contents)
				case "open stdin":
					reader, writer, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					defer reader.Close()
					defer writer.Close()
					command.Stdin = reader
					wrote = make(chan error, 1)
					go func() { _, err := writer.Write(contents); wrote <- err }()
				}
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				if size > limit {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "10 MiB write limit") || !strings.Contains(diagnostic.String(), "upload") || requests.Load() != 0 {
						t.Errorf("oversized write: error=%v requests=%d diagnostic=%q", err, requests.Load(), diagnostic.String())
					}
				} else if err != nil || requests.Load() != 1 {
					t.Errorf("valid write: error=%v requests=%d diagnostic=%q", err, requests.Load(), diagnostic.String())
				}
				if out.Len() != 0 {
					t.Errorf("unexpected stdout: %q", out.String())
				}
				if wrote != nil {
					select {
					case err := <-wrote:
						if err != nil {
							t.Errorf("pipe write: %v", err)
						}
					case <-time.After(time.Second):
						t.Error("input writer did not finish")
					}
				}
			})
		}
	}
}
