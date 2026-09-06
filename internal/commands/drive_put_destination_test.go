// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDrivePutDestinationArguments(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	source := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(source, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		args       []string
		valid      bool
	}{
		{"omitted", "files/report.txt", nil, true},
		{"explicit", "files/other.txt", []string{"files/other.txt"}, true},
		{"empty", "files/report.txt", []string{""}, false},
		{"whitespace", " ", []string{" "}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "report" || r.Method != http.MethodPut || r.URL.Path != "/api/v1/files/me/"+tc.path {
					t.Errorf("request=%s %s body=%q error=%v", r.Method, r.URL, body, err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"path": tc.path, "size": len(body)})
			}))
			defer server.Close()
			cmd := NewRoot("test")
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(append([]string{"drive", "--drive-url", server.URL, "--token", "synthetic-token", "put", source}, tc.args...))
			err := cmd.Execute()
			out, diagnostic := stdout.String(), stderr.String()
			if tc.valid {
				if err != nil || requests.Load() != 1 || !strings.Contains(diagnostic, "Uploaded "+tc.path) {
					t.Errorf("valid destination: error=%v requests=%d stderr=%q", err, requests.Load(), diagnostic)
				}
			} else if err == nil || !strings.Contains(err.Error(), "destination path cannot be empty") || requests.Load() != 0 || diagnostic != "" {
				t.Errorf("empty destination: error=%v requests=%d stderr=%q", err, requests.Load(), diagnostic)
			}
			if out != "" {
				t.Errorf("unexpected stdout=%q", out)
			}
		})
	}
}
