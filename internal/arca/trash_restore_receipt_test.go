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

func TestTrashRestoreReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `{"path":"files/item","size":7,"checksum":"opaque","extra":true}`, true},
		{"null", `null`, false},
		{"empty", `{}`, false},
		{"missing path", `{"size":7,"checksum":"opaque"}`, false},
		{"wrong path", `{"path":"files/other","size":7,"checksum":"opaque"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct{ Owner, Path string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Owner != "https://auth.latere.ai|9ab3" || body.Path != "files/item" {
					t.Errorf("request body=%+v error=%v", body, err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/trash/restore" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := New(server.URL, "synthetic-token").TrashRestore(t.Context(), "https://auth.latere.ai|9ab3", "files/item")
			if tc.valid {
				if err != nil {
					t.Errorf("valid receipt: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "trash restore receipt") || !strings.Contains(err.Error(), "outcome is unknown") {
				t.Errorf("invalid receipt: %v", err)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}
