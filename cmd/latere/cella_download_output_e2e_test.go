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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/commands"
)

func TestCellaConfiguredDownloadOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	token := filepath.Join(root, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, mode := range []string{"cat", "export"} {
			for _, writable := range []string{"1", "0"} {
				t.Run(prefix+"/"+mode+"/writable="+writable, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Error("incorrect authorization")
						}
						if mode == "cat" {
							if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/files") {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
						} else if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/files/export") {
							t.Errorf("request=%s %s", r.Method, r.URL)
						}
						_, _ = io.WriteString(w, "download\x00bytes\n")
					}))
					defer server.Close()
					output := filepath.Join(t.TempDir(), "output")
					if err := os.WriteFile(output, []byte("previous\n"), 0600); err != nil {
						t.Fatal(err)
					}
					args := []string{"-test.run=^TestCellaDownloadOutputHelperProcess$", "--", prefix, mode, "dev"}
					if mode == "cat" {
						args = append(args, "/workspace/file")
					}
					args = append(args, "--api-url", server.URL)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_DOWNLOAD_OUTPUT="+output, "LATERE_TEST_DOWNLOAD_WRITABLE="+writable)
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					want := "previous\n"
					if writable == "1" {
						want += "download\x00bytes\n"
						if err != nil || diagnostic.Len() != 0 {
							t.Errorf("output failed: %v %q", err, diagnostic.String())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || diagnostic.Len() == 0 {
						t.Errorf("missing output failure: %v %q", err, diagnostic.String())
					}
					data, readErr := os.ReadFile(output)
					if readErr != nil || string(data) != want || out.Len() != 0 {
						t.Errorf("configured file=%q stdout=%q error=%v, want file=%q", data, out.String(), readErr, want)
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d", requests.Load())
					}
				})
			}
		}
	}
}

// Exercise the full command tree and exit handling with an inherited output
// writer, independently of the process's stdout descriptor.
func TestCellaDownloadOutputHelperProcess(t *testing.T) {
	output := os.Getenv("LATERE_TEST_DOWNLOAD_OUTPUT")
	if output == "" {
		return
	}
	flags := os.O_RDONLY
	if os.Getenv("LATERE_TEST_DOWNLOAD_WRITABLE") == "1" {
		flags = os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(output, flags, 0600)
	if err != nil {
		t.Fatal(err)
	}
	root := commands.NewRoot("test")
	root.SetOut(file)
	root.SetArgs(os.Args[3:])
	err = root.Execute()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
	os.Exit(0)
}
