// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEvalRedirectPreservesOperation(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, mode := range []string{"live", "dry-run retained", "dry-run equivalent", "live equivalent", "dry-run dropped", "dry-run changed", "live changed to dry-run", "read"} {
			t.Run(strconv.Itoa(status)+"/"+mode, func(t *testing.T) {
				method, query, targetQuery := http.MethodPost, "", ""
				switch mode {
				case "dry-run retained":
					query, targetQuery = "?dry_run=1", "?dry_run=1"
				case "dry-run equivalent":
					query, targetQuery = "?dry_run=1", "?dry_run=true"
				case "live equivalent":
					targetQuery = "?dry_run=false"
				case "dry-run dropped":
					query = "?dry_run=1"
				case "dry-run changed":
					query, targetQuery = "?dry_run=1", "?dry_run=0"
				case "live changed to dry-run":
					targetQuery = "?dry_run=1"
				case "read":
					method = http.MethodGet
				}
				valid := method == http.MethodGet || status >= 307 && (query == targetQuery || mode == "dry-run equivalent" || mode == "live equivalent")
				wantBody := "suite: test\n"
				if method == http.MethodGet {
					wantBody = ""
				}
				var redirects atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if r.URL.Path == "/target" {
						redirects.Add(1)
						if valid && (r.Method != method || string(data) != wantBody || r.Header.Get("Authorization") != "Bearer synthetic-token") {
							t.Errorf("redirect altered request: %s body=%q", r.Method, data)
						}
						_, _ = io.WriteString(w, `{}`)
						return
					}
					w.Header().Set("Location", "/target"+targetQuery)
					w.WriteHeader(status)
				}))
				defer server.Close()
				client, err := newEvalClient(server.URL, "synthetic-token")
				if err != nil {
					t.Fatal(err)
				}
				var out any
				err = client.do(t.Context(), method, "/apply"+query, strings.NewReader(wantBody), "application/yaml", &out)
				if valid {
					if err != nil || redirects.Load() != 1 {
						t.Errorf("valid redirect: error=%v calls=%d", err, redirects.Load())
					}
				} else if err == nil || !strings.Contains(err.Error(), "redirect changed") || redirects.Load() != 0 {
					t.Errorf("unsafe redirect: error=%v calls=%d", err, redirects.Load())
				}
			})
		}
	}
}
