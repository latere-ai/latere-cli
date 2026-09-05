// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDrivePutRejectsNonRegularSource(t *testing.T) {
	sources := map[string]string{"directory": t.TempDir()}
	if info, err := os.Stat(os.DevNull); err == nil && !info.Mode().IsRegular() {
		sources["device"] = os.DevNull
	}
	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, `{"path":"files/destination","size":0}`)
			}))
			defer server.Close()
			_, _, err := execDrive(t, server, "put", source, "files/destination")
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("invalid upload source = %v", err)
			}
			if requests.Load() != 0 {
				t.Errorf("invalid source sent %d HTTP requests", requests.Load())
			}
		})
	}
}

func TestDrivePutAllowsSymlinkToRegularFile(t *testing.T) {
	root := t.TempDir()
	source, link := filepath.Join(root, "source"), filepath.Join(root, "alias")
	if err := os.WriteFile(source, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/me/files/alias" {
			t.Errorf("upload path = %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "content" {
			t.Errorf("symlink upload = %q, %v", body, err)
		}
		_, _ = io.WriteString(w, `{"path":"files/alias","size":7}`)
	}))
	defer server.Close()
	if _, _, err := execDrive(t, server, "put", link); err != nil {
		t.Fatal(err)
	}
}
