// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPolicyConfiguredOutput(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "synthetic-token"))
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	const body = `[{"name":"restricted","label":"Restricted","is_default":true,"selectable":true,"sidecar_required":true},{"name":"second"}]`
	const first = "policy:     restricted\nlabel:      Restricted\ndefault:    yes\nselectable: yes\nsidecar:    yes\ncapability: -\nsource:     -\ndescription:-\n"
	const second = "policy:     second\ndefault:    no\nselectable: no\nsidecar:    no\ncapability: -\nsource:     -\ndescription:-\n"
	const emptyOutput = "No policy profiles are visible to this token.\nAsk your Latere admin to assign a selectable policy, then re-run `latere cella apply` with `spec.policy` set in your Manifest.\n"
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, verb := range [][]string{{"policy"}, {"policy", "list"}, {"policies"}} {
			for _, empty := range []bool{false, true} {
				failPoints := []int{0, 1, 2, 3}
				if empty {
					failPoints = []int{0, 1}
				}
				for _, failAt := range failPoints {
					t.Run(fmt.Sprintf("%s/%v/empty=%t/failAt=%d", prefix, verb, empty, failAt), func(t *testing.T) {
						var requests atomic.Int32
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests.Add(1)
							if r.Method != http.MethodGet || r.URL.Path != "/v1/policies" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
							data := body
							if empty {
								data = "[]"
							}
							_, _ = io.WriteString(w, data)
						}))
						defer server.Close()
						sentinel := errors.New("policy output unavailable")
						out := &failingEnvWriter{failAt: failAt, err: sentinel}
						root := NewRoot("test")
						root.SetOut(out)
						root.SetErr(io.Discard)
						args := append([]string{prefix}, verb...)
						root.SetArgs(append(args, "--api-url", server.URL))
						leaked, err := captureStdout(root.Execute)
						want := first + "\n" + second
						if empty {
							want = emptyOutput
						}
						if failAt > 0 {
							want = ""
							if failAt >= 2 {
								want = first
							}
							if failAt == 3 {
								want += "\n"
							}
							if !errors.Is(err, sentinel) || out.calls != failAt {
								t.Errorf("error=%v writes=%d, want failure on write %d", err, out.calls, failAt)
							}
						} else if err != nil {
							t.Errorf("policy output: %v", err)
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
	}
}
