// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestCellaLogOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		factory func() *cobra.Command
		args    []string
		follow  bool
	}{
		{"logs", newCeLogsCmd, []string{"dev", "cmd"}, false},
		{"logs follow", newCeLogsCmd, []string{"dev", "cmd", "--follow"}, true},
		{"run logs", newCeRunLogsCmd, []string{"run"}, false},
		{"run logs follow", newCeRunLogsCmd, []string{"run", "--follow"}, true},
		{"exec", newCeExecCmd, []string{"dev", "echo"}, true},
		{"run follow", newCeRunCmd, []string{"dev", "echo", "--follow"}, true},
	} {
		for _, fail := range []bool{false, true} {
			name := tc.name + "/success"
			if fail {
				name = tc.name + "/write failure"
			}
			t.Run(name, func(t *testing.T) {
				var polls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/dev/commands" {
						_, _ = w.Write([]byte(`{"command_id":"cmd"}`))
						return
					}
					if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/logs") {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					if polls.Add(1) == 1 {
						_, _ = w.Write([]byte(`{"bytes":"first\n","next_cursor":6,"phase":"running"}`))
					} else {
						if r.URL.Query().Get("cursor") != "6" {
							t.Error("log stream did not resume from the returned cursor")
						}
						_, _ = w.Write([]byte(`{"bytes":"second\n","next_cursor":13,"phase":"exited","exit_code":0}`))
					}
				}))
				defer server.Close()
				out := &failingEnvWriter{}
				var wantErr error
				if fail {
					wantErr = errors.New("output unavailable")
					out.failAt, out.err = 1, wantErr
				}
				cmd := tc.factory()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(out)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(append([]string{"--api-url", server.URL}, tc.args...))
				if err := cmd.Execute(); !errors.Is(err, wantErr) {
					t.Errorf("output error = %v, want %v", err, wantErr)
				}
				wantPolls, wantOutput := int32(1), "first\n"
				if tc.follow && !fail {
					wantPolls, wantOutput = 2, "first\nsecond\n"
				}
				if polls.Load() != wantPolls || out.calls != int(wantPolls) {
					t.Errorf("polls=%d, writes=%d; want %d of each", polls.Load(), out.calls, wantPolls)
				}
				if !fail && out.String() != wantOutput {
					t.Errorf("output = %q, want %q", out.String(), wantOutput)
				}
			})
		}
	}
}
