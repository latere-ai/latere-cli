// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestToposAgentsConfiguredOutput(t *testing.T) {
	t.Setenv("TOPOS_TOKEN", "synthetic-token")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	const detail = `{"id":"agent-1","display_name":"Bot","kind":"worker","org_id":"org-1","owner_sub":"user-1","workspace_ref":"files/work"}`
	const record = "id:         agent-1\ndisplay_name:Bot\nkind:       worker\norg_id:     org-1\nowner:      user-1\nworkspace:  files/work\n"
	for _, tc := range []struct {
		name, method, body string
		args               []string
		chunks             []string
	}{
		{"create", "POST", detail, []string{"create", "--name", "Bot", "--kind", "worker"}, []string{"Created agent agent-1\n\n", record}},
		{"list", "GET", `{"agents":[` + detail + `,` + detail + `]}`, []string{"list"}, []string{record, "\n", record}},
		{"empty list", "GET", `{"agents":[]}`, []string{"list"}, []string{"No agents are visible to this token.\n"}},
	} {
		for failAt := 0; failAt <= len(tc.chunks); failAt++ {
			t.Run(fmt.Sprintf("%s/failAt=%d", tc.name, failAt), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != tc.method || r.URL.Path != "/v1/agents" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				sentinel := errors.New("agent output unavailable")
				out := &failingEnvWriter{failAt: failAt, err: sentinel}
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				args := append([]string{"topos", "agents"}, tc.args...)
				root.SetArgs(append(args, "--api-url", server.URL))
				leaked, err := captureStdout(root.Execute)
				want := strings.Join(tc.chunks, "")
				if failAt > 0 {
					want = strings.Join(tc.chunks[:failAt-1], "")
					if !errors.Is(err, sentinel) || out.calls != failAt {
						t.Errorf("error=%v writes=%d, want failure on write %d", err, out.calls, failAt)
					}
				} else if err != nil {
					t.Errorf("agent output: %v", err)
				}
				if out.String() != want || leaked != "" {
					t.Errorf("configured=%q process stdout=%q, want configured=%q", out.String(), leaked, want)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
