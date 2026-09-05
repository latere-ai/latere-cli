// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadsPreserveOutputOnTruncatedResponse(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "test-tok"))
	for _, command := range []string{"drive", "cella"} {
		for _, existing := range []bool{false, true} {
			t.Run(command+map[bool]string{false: "/new", true: "/existing"}[existing], func(t *testing.T) {
				dir := t.TempDir()
				dest := filepath.Join(dir, "download")
				if existing {
					if err := os.WriteFile(dest, []byte("original"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Length", "100")
					_, _ = io.WriteString(w, "partial")
				}))
				defer srv.Close()
				var err error
				if command == "drive" {
					_, _, err = execDrive(t, srv, "get", "files/download", "-o", dest)
				} else {
					cmd := newCeExportCmd()
					cmd.SetOut(new(bytes.Buffer))
					cmd.SetErr(new(bytes.Buffer))
					cmd.SetArgs([]string{"dev", "--api-url", srv.URL, "-o", dest})
					err = cmd.Execute()
				}
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("error = %v, want truncated-response error", err)
				}
				data, err := os.ReadFile(dest)
				if existing {
					if err != nil || string(data) != "original" {
						t.Errorf("existing output changed: %q, %v", data, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("failed download left output: %q, %v", data, err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if existing {
					want = 1
				}
				if len(entries) != want {
					t.Errorf("download left files: %v", entries)
				}
			})
		}
	}
}

func TestDownloadsReplaceOutputAfterCompleteResponse(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "test-tok"))
	for _, command := range []string{"drive", "cella"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "download")
			if err := os.WriteFile(dest, []byte("old"), 0o640); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "link")
			if err := os.Symlink("download", link); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "complete")
			}))
			defer srv.Close()
			var err error
			if command == "drive" {
				_, _, err = execDrive(t, srv, "get", "files/download", "-o", link)
			} else {
				cmd := newCeExportCmd()
				cmd.SetArgs([]string{"dev", "--api-url", srv.URL, "-o", link})
				err = cmd.Execute()
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(dest)
			if err != nil || string(data) != "complete" {
				t.Fatalf("output = %q, %v", data, err)
			}
			info, err := os.Stat(dest)
			if err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("permissions changed: %v, %v", info, err)
			}
			if _, err := os.Readlink(link); err != nil {
				t.Fatalf("output symlink replaced: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("scratch files remain: %v, %v", entries, err)
			}
		})
	}
}

func TestSaveDownloadResolvesParentBeforeTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "real", "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real/child", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := saveDownload(dir+"/link/../download", strings.NewReader("complete")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "real", "download"))
	if err != nil || string(data) != "complete" {
		t.Fatalf("wrong destination: %q, %v", data, err)
	}
}

func TestSaveDownloadRejectsInvalidDestination(t *testing.T) {
	dir := t.TempDir()
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink("missing", dangling); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{dir, dangling, filepath.Join(dir, "missing", "output"), filepath.Join(dir, strings.Repeat("x", 256))} {
		if err := saveDownload(dest, strings.NewReader("complete")); err == nil {
			t.Errorf("saveDownload(%q) succeeded", dest)
		}
	}
}

func TestSaveDownloadBasename(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveDownload("download", strings.NewReader("complete")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("download")
	if err != nil || string(data) != "complete" {
		t.Fatalf("output = %q, %v", data, err)
	}
}

func TestSaveDownloadUnwritableParent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := saveDownload(filepath.Join(dir, "download"), strings.NewReader("complete")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("error = %v, want permission failure", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("files left after failure: %v, %v", entries, err)
	}
}
