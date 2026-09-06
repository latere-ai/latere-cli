// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCellaExportDownloadStatus(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "synthetic-token"))
	for _, status := range []int{200, 202, 204, 206} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/existing=%t", status, existing), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/files/export") || r.Header.Get("Range") != "" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					if status == 206 {
						w.Header().Set("Content-Range", "bytes 0-7/100")
					}
					w.WriteHeader(status)
					if status != 204 {
						_, _ = io.WriteString(w, "contents")
					}
				}))
				defer server.Close()
				dir := t.TempDir()
				dest := filepath.Join(dir, "archive.tar")
				if existing {
					if err := os.WriteFile(dest, []byte("previous"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				cmd := newCeExportCmd()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(new(bytes.Buffer))
				cmd.SetErr(new(bytes.Buffer))
				cmd.SetArgs([]string{"dev", "--api-url", server.URL, "-o", dest})
				err := cmd.Execute()
				if status == 200 {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "complete download") || !strings.Contains(err.Error(), fmt.Sprint(status)) {
					t.Errorf("error=%v, want rejected HTTP %d", err, status)
				}
				data, readErr := os.ReadFile(dest)
				switch {
				case status == 200:
					if readErr != nil || string(data) != "contents" {
						t.Errorf("download=%q error=%v", data, readErr)
					}
				case existing:
					if readErr != nil || string(data) != "previous" {
						t.Errorf("existing file changed: %q %v", data, readErr)
					}
				default:
					if !errors.Is(readErr, os.ErrNotExist) {
						t.Errorf("failed download created output: %q %v", data, readErr)
					}
				}
				entries, err := os.ReadDir(dir)
				want := 0
				if existing || status == 200 {
					want = 1
				}
				if err != nil || len(entries) != want {
					t.Errorf("leftover files: %v %v", entries, err)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d", requests.Load())
				}
			})
		}
	}
}
