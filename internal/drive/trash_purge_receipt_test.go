// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTrashPurgeReceipt(t *testing.T) {
	for _, path := range []string{"", "files/item"} {
		for _, tc := range []struct {
			name, body string
			want       int
		}{
			{"zero", `{"purged":0}`, 0},
			{"one", `{"purged":1,"extra":true}`, 1},
			{"many", `{"purged":12}`, 12},
			{"null", `null`, -1},
			{"missing", `{}`, -1},
			{"null count", `{"purged":null}`, -1},
			{"negative", `{"purged":-1}`, -1},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/trash" || r.URL.Query().Get("owner") != "org:example" || r.URL.Query().Get("path") != path || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				n, err := New(server.URL, "synthetic-token").TrashPurge(t.Context(), "org:example", path)
				if tc.want >= 0 {
					if err != nil || n != tc.want {
						t.Errorf("valid receipt: count=%d error=%v", n, err)
					}
				} else if err == nil || n != 0 || !strings.Contains(err.Error(), "trash purge receipt") || !strings.Contains(err.Error(), "outcome is unknown") {
					t.Errorf("invalid receipt: count=%d error=%v", n, err)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
