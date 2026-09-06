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

func TestToposSessionConfiguredOutput(t *testing.T) {
	t.Setenv("TOPOS_TOKEN", "synthetic-token")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	const detail = `{"session_id":"sess-1","sandbox_id":"sb-1","output":"First line\nSecond line\n","stop_reason":"end_turn","tool_calls":2,"usage":{"input_tokens":10,"output_tokens":5}}`
	const metadata = "session:    sess-1\nsandbox:    sb-1\nstop_reason:end_turn\ntool_calls: 2\ntokens:     10 in / 5 out\n"
	const emptyMetadata = "session:    sess-1\nsandbox:    -\nstop_reason:-\ntool_calls: 0\ntokens:     0 in / 0 out\n"
	const row = "sess-1  awaiting_input    agent-1\n"
	for _, tc := range []struct {
		name, method, path, body string
		args                     []string
		chunks                   []string
	}{
		{"create", "POST", "/v1/agents/agent-1/sessions", detail, []string{"create", "agent-1", "--prompt", "test"}, []string{metadata, "\nFirst line\nSecond line\n\n"}},
		{"empty result", "POST", "/v1/agents/agent-1/sessions", `{"session_id":"sess-1"}`, []string{"create", "agent-1", "--prompt", "test"}, []string{emptyMetadata}},
		{"list", "GET", "/v1/sessions", `{"sessions":[{"id":"sess-1","status":"awaiting_input","agent_id":"agent-1"},{"id":"sess-1","status":"awaiting_input","agent_id":"agent-1"}]}`, []string{"ls"}, []string{row, row}},
		{"empty list", "GET", "/v1/sessions", `{"sessions":[]}`, []string{"ls"}, []string{"No interactive sessions.\n"}},
	} {
		for failAt := 0; failAt <= len(tc.chunks); failAt++ {
			t.Run(fmt.Sprintf("%s/failAt=%d", tc.name, failAt), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				sentinel := errors.New("session output unavailable")
				out := &failingEnvWriter{failAt: failAt, err: sentinel}
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				args := append([]string{"topos", "session"}, tc.args...)
				root.SetArgs(append(args, "--api-url", server.URL))
				leaked, err := captureStdout(root.Execute)
				want := strings.Join(tc.chunks, "")
				if failAt > 0 {
					want = strings.Join(tc.chunks[:failAt-1], "")
					if !errors.Is(err, sentinel) || out.calls != failAt {
						t.Errorf("error=%v writes=%d, want failure on write %d", err, out.calls, failAt)
					}
				} else if err != nil {
					t.Errorf("session output: %v", err)
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
