// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownloadRequiresCompleteStatus(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			status int
			body   string
		}{
			{"complete", 200, "file bytes"},
			{"empty file", 200, ""},
			{"accepted", 202, `{"status":"pending"}`},
			{"no content", 204, ""},
			{"partial content", 206, "fil"},
		} {
			t.Run(fmt.Sprintf("%s/redirect=%t", tc.name, redirect), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
						t.Errorf("unexpected request: %s Range=%q", r.Method, r.Header.Get("Range"))
					}
					if r.URL.Path != "/object" && redirect {
						http.Redirect(w, r, "/object", http.StatusFound)
						return
					}
					if tc.status == 206 {
						w.Header().Set("Content-Range", "bytes 0-2/10")
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				body, _, err := New(server.URL, "synthetic-token").Download(t.Context(), "me", "files/item", 0)
				if tc.status == 200 {
					if err != nil {
						t.Fatal(err)
					}
					data, readErr := io.ReadAll(body)
					_ = body.Close()
					if readErr != nil || string(data) != tc.body {
						t.Errorf("body=%q error=%v", data, readErr)
					}
				} else {
					if body != nil {
						_ = body.Close()
					}
					if err == nil || !strings.Contains(err.Error(), "complete download") || body != nil {
						t.Errorf("status=%d error=%v body=%v", tc.status, err, body)
					}
				}
				want := int32(1)
				if redirect {
					want = 2
				}
				if requests.Load() != want {
					t.Errorf("requests=%d, want %d", requests.Load(), want)
				}
			})
		}
	}
}
