// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMoveReceipt(t *testing.T) {
	const valid = `{"path":"files/to","moved_from":"files/from","extra":true}`
	for _, tc := range []struct {
		name, source, body string
		status             int
		valid              bool
	}{
		{"valid", "files/from", valid, 200, true},
		{"leading slash", "/files/from", valid, 200, true},
		{"null", "files/from", "null", 200, false},
		{"empty", "files/from", "{}", 200, false},
		{"missing destination", "files/from", `{"moved_from":"files/from"}`, 200, false},
		{"missing source", "files/from", `{"path":"files/to"}`, 200, false},
		{"wrong destination", "files/from", `{"path":"files/other","moved_from":"files/from"}`, 200, false},
		{"wrong source", "files/from", `{"path":"files/to","moved_from":"files/other"}`, 200, false},
		{"API error", "files/from", `{"error":"missing file"}`, 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct {
					Destination string `json:"move_to"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Destination != "files/to" {
					t.Errorf("request body=%+v error=%v", body, err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/files/me/files/from" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			out, err := New(server.URL, "synthetic-token").Move(t.Context(), "me", tc.source, "files/to")
			switch {
			case tc.valid:
				if err != nil || out == nil || out.Path != "files/to" || out.MovedFrom != "files/from" {
					t.Errorf("valid receipt: %+v %v", out, err)
				}
			case tc.status == 404:
				var apiErr *Error
				if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Message != "missing file" {
					t.Errorf("API error changed: %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "move receipt") || !strings.Contains(err.Error(), "outcome is unknown") || out != nil {
					t.Errorf("invalid receipt: %+v %v", out, err)
				}
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}
