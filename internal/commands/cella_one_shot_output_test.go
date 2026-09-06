// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestOneShotRunFailureDiagnostics(t *testing.T) {
	var stderr bytes.Buffer
	out := oneShotRunDTO{State: "failed", CleanupError: "delete denied", Truncated: true, Error: "execution unavailable"}
	if err := printOneShotRun(io.Discard, &stderr, out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"command failed", "sandbox cleanup failed", "delete denied", "output truncated", "execution unavailable"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("diagnostics missing %q: %s", want, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "cella deleted") {
		t.Errorf("failed cleanup reported success: %s", stderr.String())
	}
}

func TestOneShotRunOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"exited","exit_code":0,"sandbox_name":"test","stdout":"result\n","stderr":"warning\n"}`))
	}))
	defer server.Close()
	for _, mode := range []string{"success", "stdout failure", "stderr failure"} {
		t.Run(mode, func(t *testing.T) {
			stdout, stderr := &failingEnvWriter{}, &failingEnvWriter{}
			var wantErr error
			if mode != "success" {
				wantErr = errors.New("output unavailable")
				failed := stdout
				if mode == "stderr failure" {
					failed = stderr
				}
				failed.failAt, failed.err = 1, wantErr
			}
			cmd := newCeRunCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetOut(stdout)
			cmd.SetErr(stderr)
			cmd.SetArgs([]string{"--api-url", server.URL, "--ephemeral", "--rm", "--", "echo"})
			if err := cmd.Execute(); !errors.Is(err, wantErr) {
				t.Errorf("output error = %v, want %v", err, wantErr)
			}
			if stdout.calls != 1 {
				t.Errorf("stdout writes = %d, want 1", stdout.calls)
			}
			if mode == "stdout failure" {
				if stderr.calls != 0 {
					t.Error("printed diagnostics after stdout failure")
				}
			} else {
				if stdout.String() != "result\n" {
					t.Errorf("stdout = %q", stdout.String())
				}
				if stderr.calls != 1 {
					t.Errorf("stderr writes = %d, want 1", stderr.calls)
				}
			}
			if mode == "success" && (!strings.HasPrefix(stderr.String(), "warning\n") || !strings.Contains(stderr.String(), "✓ command exited 0") || !strings.Contains(stderr.String(), "✓ cella deleted")) {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}
