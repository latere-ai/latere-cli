// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package arca

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
	// The receipt is the object now current at the path. It does not echo
	// the revision that was asked for, so the path and the checksum are
	// what a caller can hold the answer to.
	const valid = `{"path":"files/item","size":7,"checksum":"opaque","extra":true}`
	for _, tc := range []struct {
		name, path, body string
		valid            bool
	}{
		{"valid", "files/item", valid, true},
		{"leading slash", "/files/item", valid, true},
		{"null", "files/item", "null", false},
		{"empty", "files/item", "{}", false},
		{"missing path", "files/item", `{"size":7,"checksum":"opaque"}`, false},
		{"wrong path", "files/item", `{"path":"files/other","size":7,"checksum":"opaque"}`, false},
		{"missing checksum", "files/item", `{"path":"files/item","size":7}`, false},
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
				if r.Method != http.MethodPost || r.URL.Path != "/v1/files/me/files/item" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			out, err := New(server.URL, "synthetic-token").RestoreVersion(t.Context(), "me", tc.path, 3)
			if tc.valid {
				if err != nil || out == nil || out.Path != "files/item" || out.Size != 7 || out.Checksum != "opaque" {
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
