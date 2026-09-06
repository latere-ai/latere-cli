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

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestCellaRunIDOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	for _, detached := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			name, id := "background", "cmd-123"
			if detached {
				name, id = "detached", "run-123"
			}
			if fail {
				name += "/write failure"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if detached {
						_, _ = w.Write([]byte(`{"run_id":"run-123","state":"creating"}`))
					} else {
						_, _ = w.Write([]byte(`{"command_id":"cmd-123","phase":"running"}`))
					}
				}))
				defer server.Close()
				out := &failingEnvWriter{}
				var wantErr error
				if fail {
					wantErr = errors.New("output unavailable")
					out.failAt, out.err = 1, wantErr
				}
				args := []string{"--api-url", server.URL, "dev", "--", "echo"}
				if detached {
					args = []string{"--api-url", server.URL, "--ephemeral", "--rm", "--detach", "--", "echo"}
				}
				cmd := newCeRunCmd()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(out)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				err := cmd.Execute()
				if !errors.Is(err, wantErr) {
					t.Errorf("output error = %v, want %v", err, wantErr)
				}
				if fail && (err == nil || !strings.Contains(err.Error(), id) || !strings.Contains(err.Error(), "started")) {
					t.Errorf("write error omitted recovery ID or start status: %v", err)
				}
				if out.calls != 1 || (!fail && out.String() != id+"\n") {
					t.Errorf("ID output = %q, writes=%d", out.String(), out.calls)
				}
				if requests.Load() != 1 {
					t.Errorf("start requests = %d, want 1", requests.Load())
				}
			})
		}
	}
}
