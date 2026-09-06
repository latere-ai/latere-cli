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
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
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

func TestCollectCellaUploadParentPaths(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "actual")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		filepath.Join(parent, "file"): "wanted",
		filepath.Join(root, "file"):   "wrong file",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, tc := range []struct{ name, cwd, source, remote string }{
		{"parent", child, "..", "actual/file"},
		{"current", parent, ".", "file"},
		{"current trailing slash", parent, "./", "file"},
		{"current repeated dot", parent, "./.", "file"},
		{"relative symlink parent", root, "link/..", "actual/file"},
		{"absolute symlink parent", root, link + "/..", "actual/file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cwd)
			files, err := collectCellaUploadFiles([]string{tc.source})
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("files = %v, want one file", files)
			}
			if files[0].rel != tc.remote {
				t.Errorf("remote path = %q, want %q", files[0].rel, tc.remote)
			}
			data, err := os.ReadFile(files[0].local)
			if err != nil || string(data) != "wanted" {
				t.Errorf("local content = %q, %v; want wanted", data, err)
			}
		})
	}
}
