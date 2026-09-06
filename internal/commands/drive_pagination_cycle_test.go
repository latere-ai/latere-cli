// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDrivePaginationCycles(t *testing.T) {
	for _, verb := range []struct {
		name, path string
		args       []string
	}{
		{"files", "/api/v1/files/me/files", []string{"ls"}},
		{"trash", "/api/v1/trash", []string{"ls", "--trashed"}},
		{"history", "/api/v1/files/me/files/item", []string{"history", "files/item"}},
		{"shares", "/api/v1/shares", []string{"shares"}},
		{"inbox", "/api/v1/shared-with-me", []string{"shares", "--inbox"}},
	} {
		for _, tc := range []struct {
			name  string
			next  []string
			valid bool
		}{
			{"valid with empty page", []string{"a", "b", ""}, true},
			{"same cursor", []string{"a", "a"}, false},
			{"long cycle", []string{"a", "b", "a"}, false},
		} {
			t.Run(verb.name+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					i := int(requests.Add(1)) - 1
					// Stop the unfixed loop deterministically after it requests a repeated page.
					if i >= len(tc.next) {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					wantCursor := ""
					if i > 0 {
						wantCursor = tc.next[i-1]
					}
					if r.Method != http.MethodGet || r.URL.Path != verb.path || r.URL.Query().Get("cursor") != wantCursor {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					}
					entries := []map[string]any{}
					if i != 1 {
						entries = append(entries, map[string]any{"path": "files/item", "id": "share", "version_no": i + 1})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries, "next_cursor": tc.next[i]})
				}))
				defer server.Close()
				cmd := newDriveCmd()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				var out, diagnostic bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&diagnostic)
				args := append([]string{"--drive-url", server.URL, "--token", "synthetic-token", "--json"}, verb.args...)
				cmd.SetArgs(args)
				err := cmd.Execute()
				if tc.valid {
					var entries []any
					if err != nil || json.Unmarshal(out.Bytes(), &entries) != nil || len(entries) != 2 {
						t.Errorf("valid pagination: error=%v output=%q", err, out.String())
					}
				} else if err == nil || !strings.Contains(err.Error(), "pagination returned a repeated cursor") || out.Len() != 0 {
					t.Errorf("cycle: error=%v output=%q", err, out.String())
				}
				if requests.Load() != int32(len(tc.next)) {
					t.Errorf("requests=%d, want %d", requests.Load(), len(tc.next))
				}
			})
		}
	}
}
