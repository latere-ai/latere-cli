// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/latere-ai/latere-cli/internal/drive"
)

func TestDriveRmPurgeError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"success", 200, `{"purged":1}`, ""},
		{"not in trash", 404, `{"error":"not in trash"}`, "not in trash"},
		{"forbidden", 403, `{"error":"purge forbidden"}`, "purge forbidden"},
		{"server failure", 500, `{"error":"purge failed"}`, "purge failed"},
		{"invalid response", 200, `{"purged":`, "unexpected EOF"},
		{"extra response", 200, `{"purged":1} {}`, "multiple JSON values"},
		{"nothing purged", 200, `{"purged":0}`, "live lookup missed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var live, trash atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				switch r.URL.Path {
				case "/api/v1/files/org/files/item":
					live.Add(1)
					if r.URL.Query().Get("permanent") != "true" {
						t.Error("missing permanent flag")
					}
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"error":"live lookup missed"}`)
				case "/api/v1/trash":
					trash.Add(1)
					if r.URL.Query().Get("owner") != "org" || r.URL.Query().Get("path") != "files/item" {
						t.Errorf("purge query=%s", r.URL.RawQuery)
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				default:
					t.Errorf("unexpected path=%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			cmd := newDriveCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			var out, diagnostic bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostic)
			cmd.SetArgs([]string{"rm", "files/item", "--permanent", "--owner", "org", "--drive-url", server.URL, "--token", "synthetic-token"})
			err := cmd.Execute()
			if tc.want == "" {
				if err != nil || diagnostic.String() != "Permanently deleted files/item\n" {
					t.Errorf("success: %v %q", err, diagnostic.String())
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.want) || diagnostic.Len() != 0 {
					t.Errorf("error=%v stderr=%q, want %q", err, diagnostic.String(), tc.want)
				}
				if tc.status >= 400 {
					var apiErr *drive.Error
					if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
						t.Errorf("lost API status: %v", err)
					}
				}
			}
			if out.Len() != 0 || live.Load() != 1 || trash.Load() != 1 {
				t.Errorf("stdout=%q live=%d trash=%d", out.String(), live.Load(), trash.Load())
			}
		})
	}
}
