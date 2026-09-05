// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectCellaUploadFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dist")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(root, "standalone"), filepath.Join(dir, "nested", "file"), filepath.Join(dir, "empty")}
	for _, path := range paths {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := collectCellaUploadFiles([]string{paths[0], dir})
	if err != nil {
		t.Fatal(err)
	}
	want := []cellaUploadFile{{"standalone", paths[0]}, {"dist/empty", paths[2]}, {"dist/nested/file", paths[1]}}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("collected files = %v, want %v", files, want)
	}
	for _, sources := range [][]string{nil, {t.TempDir()}, {filepath.Join(root, "missing")}} {
		if _, err := collectCellaUploadFiles(sources); err == nil {
			t.Error("empty or missing source accepted")
		}
	}
	if info, err := os.Stat(os.DevNull); err == nil && !info.Mode().IsRegular() {
		if _, err := collectCellaUploadFiles([]string{os.DevNull}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("device source = %v", err)
		}
	}
}

func TestCollectCellaUploadSymlinks(t *testing.T) {
	for _, target := range []string{"file", "directory", "missing"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "dist")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "target")
			switch target {
			case "file":
				if err := os.WriteFile(destination, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(dir, "alias")
			if err := os.Symlink(destination, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			for _, source := range []string{link, dir} {
				files, err := collectCellaUploadFiles([]string{source})
				if target == "file" {
					if err != nil || len(files) != 1 || files[0].local != link {
						t.Errorf("regular-file symlink = %v, %v", files, err)
					}
				} else if err == nil {
					t.Error("invalid symlink source accepted")
				}
			}
		})
	}
}
