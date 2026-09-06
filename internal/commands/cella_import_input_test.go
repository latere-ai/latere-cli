// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSniffImportZipSignatures(t *testing.T) {
	for _, third := range []byte{0x03, 0x05, 0x07} {
		for _, fourth := range []byte{0x04, 0x06, 0x08} {
			t.Run(fmt.Sprintf("PK%02x%02x", third, fourth), func(t *testing.T) {
				data := []byte{'P', 'K', third, fourth, 'd', 'a', 't', 'a'}
				path := filepath.Join(t.TempDir(), "input")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				want := importInputRegularFile
				if fourth == third+1 {
					want = importInputZip
				}
				got, err := classifyImportInput("input", f)
				if err != nil || got != want {
					t.Errorf("classification = %v, %v; want %v", got, err, want)
				}
				pos, err := f.Seek(0, io.SeekCurrent)
				if err != nil || pos != 0 {
					t.Errorf("input position = %d, %v; want 0", pos, err)
				}
			})
		}
	}
}

func TestClassifyImportInputRequiresRegularFile(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().IsRegular() {
		t.Skip("null device reports regular-file mode")
	}
	for _, name := range []string{"input", "input.tar", "input.zip"} {
		if _, err := classifyImportInput(name, file); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("special file with name %q = %v", name, err)
		}
	}
}

func TestClassifyImportInputFormats(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "plain")
	if err := os.WriteFile(plain, []byte("ordinary data"), 0600); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := zip.NewWriter(zf).Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		want       importInputKind
	}{
		{"ordinary", plain, importInputRegularFile},
		{"archive.tar", plain, importInputTar},
		{"archive.zip", zipPath, importInputZip},
		{"without-extension", zipPath, importInputZip},
		{"tar-without-extension", filepath.Join("testdata", "import", "payload.tar"), importInputTar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := classifyImportInput(tc.name, f)
			if err != nil || got != tc.want {
				t.Errorf("classification = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	closed, err := os.Open(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := classifyImportInput("input", closed); err == nil {
		t.Fatal("closed input accepted")
	}
	writeOnly, err := os.OpenFile(plain, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writeOnly.Close()
	if _, err := classifyImportInput("input", writeOnly); err == nil {
		t.Fatal("unreadable input accepted")
	}
}
