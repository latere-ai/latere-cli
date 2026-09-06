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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/commands"
)

func TestCellaCommandStatusOutputE2E(t *testing.T) {
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
		for _, tc := range []struct {
			name, mode, response, status, stdout string
			exitCode                             int
		}{
			{"successful wait", "wait", `{"phase":"exited","exit_code":0}`, "phase=exited exit_code=0\n", "", 0},
			{"failed wait", "wait", `{"phase":"exited","exit_code":7}`, "phase=exited exit_code=7\n", "", 7},
			{"logs", "logs", `{"phase":"exited","next_cursor":42,"bytes":"command logs"}`, "[cursor=42 phase=exited]\n", "command logs", 0},
		} {
			for _, writable := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/writable=%t", prefix, tc.name, writable), func(t *testing.T) {
					dir := t.TempDir()
					status := filepath.Join(dir, "status")
					if err := os.WriteFile(status, []byte("previous\n"), 0600); err != nil {
						t.Fatal(err)
					}
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Error("incorrect authorization")
						}
						_, _ = io.WriteString(w, tc.response)
					}))
					defer server.Close()
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, "-test.run=^TestCellaStatusOutputHelperProcess$", "--", prefix, tc.mode, "dev", "cmd-1", "--api-url", server.URL)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_STATUS_OUTPUT="+status, fmt.Sprintf("LATERE_TEST_STATUS_WRITABLE=%t", writable))
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					wantFile, wantExit := "previous\n", tc.exitCode
					if writable {
						wantFile += tc.status
					} else if wantExit == 0 {
						wantExit = 1
					}
					exitCode := 0
					if err != nil {
						exit, ok := errors.AsType[*exec.ExitError](err)
						if !ok {
							t.Fatal(err)
						}
						exitCode = exit.ExitCode()
					}
					if exitCode != wantExit {
						t.Errorf("exit=%d want=%d stderr=%q", exitCode, wantExit, diagnostic.String())
					}
					if !writable && tc.exitCode == 0 {
						if !strings.Contains(diagnostic.String(), "write command status") {
							t.Errorf("missing output error: %q", diagnostic.String())
						}
					} else if diagnostic.Len() != 0 {
						t.Errorf("status leaked to process stderr: %q", diagnostic.String())
					}
					got, err := os.ReadFile(status)
					if err != nil || string(got) != wantFile {
						t.Errorf("status file=%q want=%q error=%v", got, wantFile, err)
					}
					if out.String() != tc.stdout || requests.Load() != 1 {
						t.Errorf("stdout=%q want=%q requests=%d", out.String(), tc.stdout, requests.Load())
					}
				})
			}
		}
	}
}

func TestCellaStatusOutputHelperProcess(t *testing.T) {
	path := os.Getenv("LATERE_TEST_STATUS_OUTPUT")
	if path == "" {
		return
	}
	flags := os.O_RDONLY
	if os.Getenv("LATERE_TEST_STATUS_WRITABLE") == "true" {
		flags = os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		t.Fatal(err)
	}
	root := commands.NewRoot("test")
	root.SetErr(file)
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
