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

func TestLuxInvokeConfiguredOutput(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, tc := range []struct{ provider, path, body string }{
		{"openai", "/openai/v1/chat/completions", `{"choices":[{"message":{"content":"first\nsecond"}}]}`},
		{"anthropic", "/anthropic/v1/messages", `{"content":[{"type":"text","text":"first\nsecond"}]}`},
	} {
		for _, verb := range []string{"invoke", "chat"} {
			for _, jsonF := range []bool{false, true} {
				for _, failAfter := range []int{-1, 0, 3} {
					t.Run(fmt.Sprintf("%s/%s/json=%t/failAfter=%d", tc.provider, verb, jsonF, failAfter), func(t *testing.T) {
						var requests atomic.Int32
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests.Add(1)
							if r.Method != http.MethodPost || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer synthetic-token" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
							_, _ = io.WriteString(w, " \n"+tc.body+"\n ")
						}))
						defer server.Close()
						out := &evalOutputWriter{}
						sentinel := errors.New("invoke output unavailable")
						if failAfter >= 0 {
							out.remaining, out.err = failAfter, sentinel
						}
						root := NewRoot("test")
						root.SetOut(out)
						root.SetErr(io.Discard)
						args := []string{"lux", verb, "test", "--model", "test-model", "--provider", tc.provider, "--lux-url", server.URL, "--token", "synthetic-token"}
						if jsonF {
							args = append(args, "--json")
						}
						root.SetArgs(args)
						leaked, err := captureStdout(root.Execute)
						want := "first\nsecond\n"
						if jsonF {
							want = strings.TrimSpace(tc.body) + "\n"
						}
						if failAfter >= 0 {
							want = want[:failAfter]
							if !errors.Is(err, sentinel) {
								t.Errorf("output error=%v", err)
							}
						} else if err != nil {
							t.Errorf("invoke output: %v", err)
						}
						if out.String() != want {
							t.Errorf("output=%q, want %q", out.String(), want)
						}
						if leaked != "" {
							t.Errorf("result leaked to process stdout: %q", leaked)
						}
						if requests.Load() != 1 {
							t.Errorf("requests=%d, want %d", requests.Load(), 1)
						}
					})
				}
			}
		}
	}
}
