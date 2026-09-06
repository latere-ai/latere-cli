// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/commands"
)

func TestDriveListOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, long := range []bool{false, true} {
		for _, empty := range []bool{false, true} {
			for _, mode := range []string{"writable", "read-only", "transient"} {
				name := fmt.Sprintf("long=%t/empty=%t/%s", long, empty, mode)
				t.Run(name, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Method != http.MethodGet || r.URL.Path != "/api/v1/files/me/files" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Errorf("request=%s %s", r.Method, r.URL)
						}
						body := `{"entries":[{"path":"files/one","size":1},{"path":"files/two","size":2}]}`
						if empty {
							body = `{"entries":[]}`
						}
						_, _ = io.WriteString(w, body)
					}))
					defer server.Close()
					output := filepath.Join(t.TempDir(), "output")
					if err := os.WriteFile(output, []byte("previous\n"), 0600); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					args := []string{"-test.run=^TestDriveListOutputHelperProcess$", "--", "drive", "ls", "--drive-url", server.URL, "--token", "synthetic-token"}
					if long {
						args = append(args, "--long")
					}
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_LIST_OUTPUT="+output, "LATERE_TEST_LIST_MODE="+mode)
					var leaked, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &leaked, &diagnostic
					err := command.Run()
					if leaked.Len() != 0 {
						t.Errorf("leaked stdout=%q", leaked.String())
					}
					want := "previous\n"
					if mode == "writable" {
						if !empty {
							if long {
								want += "1      files/one\n2      files/two\n"
							} else {
								want += "files/one\nfiles/two\n"
							}
						}
						if err != nil || diagnostic.Len() != 0 {
							t.Errorf("output failed: %v %q", err, diagnostic.String())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || diagnostic.Len() == 0 {
						t.Errorf("ignored output failure: %v %q", err, diagnostic.String())
					}
					data, readErr := os.ReadFile(output)
					if readErr != nil || string(data) != want {
						t.Errorf("output=%q read=%v, want %q", data, readErr, want)
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d, want %d", requests.Load(), 1)
					}
				})
			}
		}
	}
}

// Fail once, then permit writes, to detect errors lost during a table flush.
type transientListWriter struct {
	out    io.Writer
	failed bool
}

func (w *transientListWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, io.ErrClosedPipe
	}
	return w.out.Write(p)
}

func TestDriveListOutputHelperProcess(t *testing.T) {
	path := os.Getenv("LATERE_TEST_LIST_OUTPUT")
	if path == "" {
		return
	}
	flags := os.O_WRONLY | os.O_APPEND
	mode := os.Getenv("LATERE_TEST_LIST_MODE")
	if mode == "read-only" {
		flags = os.O_RDONLY
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var out io.Writer = file
	if mode == "transient" {
		out = &transientListWriter{out: file}
	}
	root := commands.NewRoot("test")
	root.SetOut(out)
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
