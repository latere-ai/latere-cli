// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"archive/zip"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The one-file commands resolve a relative path under /workspace and reach
// the file routes with it.
func TestCellaOneFileCommands(t *testing.T) {
	f := newFakeCore(t)
	if out, _, err := f.runCella("", "cat", "dev", "out.log"); err != nil || out != "file content\n" {
		t.Fatalf("cat = %q, %v", out, err)
	}
	if _, _, err := f.runCella("note\n", "write", "dev", "/workspace/note.txt"); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, _, err := f.runCella("", "ls", "dev", ".")
	if err != nil || out != "0755\t96\tsrc/\n0644\t42\tgo.mod\n" {
		t.Fatalf("ls = %q, %v", out, err)
	}
	for _, args := range [][]string{
		{"mkdir", "dev", "build"},
		{"rm", "dev", "/workspace/old"},
		{"mv", "dev", "a.txt", "b.txt"},
	} {
		if _, _, err := f.runCella("", args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	want := []string{
		"GET /sandboxes/dev/files/content?path=/workspace/out.log",
		"PUT /sandboxes/dev/files?path=/workspace/note.txt",
		"GET /sandboxes/dev/files/list?path=/workspace",
		"POST /sandboxes/dev/files/mkdir",
		"DELETE /sandboxes/dev/files?path=/workspace/old",
		"POST /sandboxes/dev/files/move",
	}
	if got := f.seen(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if body := f.last("PUT", "/sandboxes/dev/files").Body; string(body) != "note\n" {
		t.Errorf("write body = %q", body)
	}
	if body := f.last("POST", "/sandboxes/dev/files/mkdir").Body; string(body) != `{"path":"/workspace/build"}` {
		t.Errorf("mkdir body = %s", body)
	}
	if body := f.last("POST", "/sandboxes/dev/files/move").Body; string(body) != `{"from":"/workspace/a.txt","to":"/workspace/b.txt"}` {
		t.Errorf("move body = %s", body)
	}
}

func TestCellaWriteFromFile(t *testing.T) {
	f := newFakeCore(t)
	src := filepath.Join(t.TempDir(), "app.cfg")
	if err := os.WriteFile(src, []byte("key=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.runCella("", "write", "dev", "app.cfg", "-f", src); err != nil {
		t.Fatalf("write: %v", err)
	}
	if req := f.last("PUT", "/sandboxes/dev/files"); string(req.Body) != "key=value\n" || req.Query["path"][0] != "/workspace/app.cfg" {
		t.Errorf("write = %s %q", req, req.Body)
	}
	if _, _, err := f.runCella("", "write", "dev", "x", "-f", filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("write from a missing file succeeded")
	}
}

func TestCellaExport(t *testing.T) {
	f := newFakeCore(t)
	out, _, err := f.runCella("", "export", "dev", "src", "/etc/hosts")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got := tarEntries(t, []byte(out)); got["workspace/main.go"] != "package main\n" {
		t.Errorf("export entries = %v", got)
	}
	if got := f.seen()[0]; got != "GET /sandboxes/dev/files?path=/etc/hosts&path=/workspace/src" {
		t.Errorf("export request = %s", got)
	}

	dest := filepath.Join(t.TempDir(), "ws.tar")
	if _, _, err := f.runCella("", "export", "dev", "--src-dir", "results", "-o", dest); err != nil {
		t.Fatalf("export -o: %v", err)
	}
	if got := f.seen()[1]; got != "GET /sandboxes/dev/files?path=/workspace/results" {
		t.Errorf("export request = %s", got)
	}
	body, err := os.ReadFile(dest)
	if err != nil || tarEntries(t, body)["workspace/main.go"] != "package main\n" {
		t.Errorf("export file: %v", err)
	}
	if _, _, err := f.runCella("", "export", "dev", "-o", ""); err == nil {
		t.Error("export with an empty --output succeeded")
	}
}

// A failure the control plane reports after the first byte, in the trailer,
// fails the export and leaves no archive that looks whole.
func TestCellaExportTrailerFailure(t *testing.T) {
	f := newFakeCore(t)
	f.exportFailure = "read_failed"
	dest := filepath.Join(t.TempDir(), "ws.tar")
	_, _, err := f.runCella("", "export", "dev", "-o", dest)
	if err == nil || !strings.Contains(err.Error(), "read_failed") {
		t.Fatalf("err = %v, want the trailer's failure", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("a failed export left %s behind: %v", dest, statErr)
	}
}

func TestCellaImport(t *testing.T) {
	dir := t.TempDir()
	single := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(single, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	zipped := filepath.Join(dir, "app.zip")
	zf, err := os.Create(zipped)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("app/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("package app")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		dest  string
		files map[string]string
	}{
		{"single file", []string{"import", "dev", "--input", single}, "/workspace", map[string]string{"notes.txt": "notes"}},
		{"zip", []string{"import", "dev", "--input", zipped, "--dest", "app"}, "/workspace/app", map[string]string{"app/main.go": "package app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCore(t)
			out, _, err := f.runCella("", tc.args...)
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			req := f.last("PUT", "/sandboxes/dev/files")
			if req.ContentType != "application/x-tar" || req.Query["dest"][0] != tc.dest {
				t.Errorf("import request = %s as %q", req, req.ContentType)
			}
			if got := tarEntries(t, req.Body); !maps.Equal(got, tc.files) {
				t.Errorf("imported %v, want %v", got, tc.files)
			}
			var receipt map[string]any
			if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt["dest"] != tc.dest || receipt["bytes"].(float64) != float64(len(req.Body)) {
				t.Errorf("receipt = %q (%v)", out, err)
			}
		})
	}
}

func TestCellaImportFromStdin(t *testing.T) {
	f := newFakeCore(t)
	archive := tarOf(t, map[string]string{"a.txt": "A"})
	if _, _, err := f.runCella(string(archive), "import", "dev"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := tarEntries(t, f.last("PUT", "/sandboxes/dev/files").Body); got["a.txt"] != "A" {
		t.Errorf("imported %v", got)
	}
}

func TestCellaUploadKeepsFolders(t *testing.T) {
	f := newFakeCore(t)
	dir := t.TempDir()
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(filepath.Join(dist, "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, content := range map[string]string{"index.html": "<html>", "js/app.js": "app()"} {
		if err := os.WriteFile(filepath.Join(dist, p), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := f.runCella("", "upload", "dev", dist, "--dest", "site")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	req := f.last("PUT", "/sandboxes/dev/files")
	if req.Query["dest"][0] != "/workspace/site" {
		t.Errorf("upload request = %s", req)
	}
	want := map[string]string{"dist/index.html": "<html>", "dist/js/app.js": "app()"}
	if got := tarEntries(t, req.Body); !maps.Equal(got, want) {
		t.Errorf("uploaded %v, want %v", got, want)
	}
	if !strings.Contains(out, "uploaded 2 files (11 bytes) to /workspace/site") {
		t.Errorf("upload output = %q", out)
	}
}
