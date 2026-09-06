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

func TestLuxUsageOutputFailures(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, mode := range []string{"empty", "table", "chart"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				body := `{"items":[]}`
				switch r.URL.Path {
				case "/lux/v1/usage":
					if mode != "empty" {
						body = `{"items":[{"group":"alpha","calls":1,"tokens_in":20,"tokens_out":10,"cost_usd_micro":2000000}]}`
					}
				case "/lux/v1/usage/series":
					if mode == "chart" {
						body = `{"items":[{"ts":"2026-07-10T00:00:00Z","cost_usd_micro":2000000},{"ts":"2026-07-11T00:00:00Z","cost_usd_micro":1000000}]}`
					}
				default:
					t.Errorf("unexpected path=%s", r.URL.Path)
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			execute := func(out io.Writer) error {
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				root.SetArgs([]string{"lux", "usage", "--lux-url", server.URL, "--token", "synthetic-token"})
				return root.Execute()
			}
			baseline := &failingEnvWriter{}
			if err := execute(baseline); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(baseline.String(), "Usage last month") {
				t.Fatalf("missing summary: %q", baseline.String())
			}
			if mode == "empty" {
				if !strings.HasSuffix(baseline.String(), "No usage in this period.\n") {
					t.Fatalf("empty usage: %q", baseline.String())
				}
			} else if !strings.Contains(baseline.String(), "$2.00, 1 calls") || !strings.Contains(baseline.String(), "20 in / 10 out") {
				t.Fatalf("missing usage: %q", baseline.String())
			}
			if mode == "chart" && (!strings.Contains(baseline.String(), "Jul 10") || !strings.Contains(baseline.String(), "Jul 11") || !strings.Contains(baseline.String(), "▇")) {
				t.Fatalf("missing chart: %q", baseline.String())
			}
			for failAt := 1; failAt <= baseline.calls; failAt++ {
				t.Run(fmt.Sprint(failAt), func(t *testing.T) {
					sentinel := errors.New("usage output unavailable")
					out := &failingEnvWriter{failAt: failAt, err: sentinel}
					err := execute(out)
					if !errors.Is(err, sentinel) || out.calls != failAt {
						t.Errorf("error=%v writes=%d, want failure on write %d", err, out.calls, failAt)
					}
					if !strings.HasPrefix(baseline.String(), out.String()) {
						t.Errorf("output continued after failure: %q", out.String())
					}
				})
			}
			if want := int32(2 * (baseline.calls + 1)); requests.Load() != want {
				t.Errorf("requests=%d, want %d", requests.Load(), want)
			}
		})
	}
}
