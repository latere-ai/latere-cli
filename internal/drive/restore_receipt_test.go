// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRestoreReceipt(t *testing.T) {
	const valid = `{"path":"files/item","restored_version":3,"size":7,"checksum":"opaque","extra":true}`
	for _, tc := range []struct {
		name, path, body string
		valid            bool
	}{
		{"valid", "files/item", valid, true},
		{"leading slash", "/files/item", valid, true},
		{"null", "files/item", "null", false},
		{"empty", "files/item", "{}", false},
		{"missing path", "files/item", `{"restored_version":3}`, false},
		{"wrong path", "files/item", `{"path":"files/other","restored_version":3}`, false},
		{"missing version", "files/item", `{"path":"files/item"}`, false},
		{"wrong version", "files/item", `{"path":"files/item","restored_version":2}`, false},
		{"zero version", "files/item", `{"path":"files/item","restored_version":0}`, false},
		{"negative version", "files/item", `{"path":"files/item","restored_version":-1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct {
					Version int `json:"restore_version"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version != 3 {
					t.Errorf("request body=%+v error=%v", body, err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/files/me/files/item" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			out, err := New(server.URL, "synthetic-token").RestoreVersion(t.Context(), "me", tc.path, 3)
			if tc.valid {
				if err != nil || out == nil || out.Path != "files/item" || out.RestoredVersion != 3 || out.Size != 7 || out.Checksum != "opaque" {
					t.Errorf("valid receipt: %+v %v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "restore receipt") || !strings.Contains(err.Error(), "outcome is unknown") || out != nil {
				t.Errorf("invalid receipt: %+v %v", out, err)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}
