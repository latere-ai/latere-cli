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

func TestLuxCatalogConfiguredOutput(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	const item = `{"model":"test-model","provider":"test","status":"active"}`
	const rate = `{"model":"test-model","provider":"test","input_usd_per_m":2,"output_usd_per_m":3}`
	const record = "model:      test-model\nprovider:   test\nstatus:     active\n"
	for _, tc := range []struct {
		name, body, record, empty string
		requests                  int32
	}{
		{"models", item, record + "rate:       $2/M in, $3/M out\n", "No models.\n", 2},
		{"providers", item, record, "No providers Lux can route to.\n", 1},
		{"rates", rate, "model:      test-model\nprovider:   test\ninput_usd_per_m:2\noutput_usd_per_m:3\n", "No model rate card.\n", 1},
	} {
		for _, empty := range []bool{false, true} {
			chunks := []string{tc.record, "\n", tc.record}
			if empty {
				chunks = []string{tc.empty}
			}
			for failAt := 0; failAt <= len(chunks); failAt++ {
				t.Run(fmt.Sprintf("%s/empty=%t/failAt=%d", tc.name, empty, failAt), func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Errorf("request=%s %s", r.Method, r.URL)
						}
						switch r.URL.Path {
						case "/lux/v1/" + tc.name:
							if empty {
								_, _ = io.WriteString(w, `{"items":[]}`)
							} else {
								_, _ = io.WriteString(w, `{"items":[`+tc.body+`,`+tc.body+`]}`)
							}
						case "/lux/v1/rates":
							_, _ = io.WriteString(w, `{"items":[`+rate+`]}`)
						default:
							t.Errorf("unexpected path=%s", r.URL.Path)
							w.WriteHeader(404)
						}
					}))
					defer server.Close()
					sentinel := errors.New("catalog output unavailable")
					out := &failingEnvWriter{failAt: failAt, err: sentinel}
					root := NewRoot("test")
					root.SetOut(out)
					root.SetErr(io.Discard)
					root.SetArgs([]string{"lux", tc.name, "--lux-url", server.URL, "--token", "synthetic-token"})
					leaked, err := captureStdout(root.Execute)
					want := strings.Join(chunks, "")
					if failAt > 0 {
						want = strings.Join(chunks[:failAt-1], "")
						if !errors.Is(err, sentinel) || out.calls != failAt {
							t.Errorf("error=%v writes=%d, want failure on write %d", err, out.calls, failAt)
						}
					} else if err != nil {
						t.Errorf("catalog output: %v", err)
					}
					if out.String() != want || leaked != "" {
						t.Errorf("configured=%q process stdout=%q, want configured=%q", out.String(), leaked, want)
					}
					if requests.Load() != tc.requests {
						t.Errorf("requests=%d, want %d", requests.Load(), tc.requests)
					}
				})
			}
		}
	}
}
