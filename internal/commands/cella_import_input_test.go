// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
