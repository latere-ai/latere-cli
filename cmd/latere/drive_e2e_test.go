// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDriveRoundtripE2E drives the built binary through the whole file
// lifecycle against a stateful fake Drive: put → ls → get → rm →
// restore → rm --permanent, plus a multipart upload past the 16 MiB
// threshold. This is the binary-level cousin of the package tests in
// internal/drive and internal/commands.
func TestDriveRoundtripE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e is slow; skipped with -short")
	}
	fake := newFakeDrive(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	fake.base = srv.URL

	dir := t.TempDir()
	src := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(src, []byte("roundtrip"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("go", append([]string{"run", ".", "drive",
			"--drive-url", srv.URL, "--token", "e2e-token"}, args...)...)
		cmd.Env = append(os.Environ(), "OTEL_SDK_DISABLED=true")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("latere drive %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	// put → ls → get
	run("put", src, "files/e2e/small.txt")
	if out := run("ls", "files/e2e"); !strings.Contains(out, "files/e2e/small.txt") {
		t.Fatalf("ls missing uploaded file:\n%s", out)
	}
	if out := run("get", "files/e2e/small.txt", "-o", "-"); !strings.Contains(out, "roundtrip") {
		t.Fatalf("get returned %q", out)
	}

	// rm → trashed → restore → verify content survives
	run("rm", "files/e2e/small.txt")
	if out := run("ls", "--trashed"); !strings.Contains(out, "files/e2e/small.txt") {
		t.Fatalf("trash listing missing file:\n%s", out)
	}
	run("restore", "files/e2e/small.txt")
	if out := run("get", "files/e2e/small.txt", "-o", "-"); !strings.Contains(out, "roundtrip") {
		t.Fatalf("restored content = %q", out)
	}

	// permanent delete leaves nothing behind
	run("rm", "files/e2e/small.txt", "--permanent")
	if out := run("ls", "files/e2e"); strings.Contains(out, "small.txt") {
		t.Fatalf("file survived permanent delete:\n%s", out)
	}

	// multipart: a file past the 16 MiB threshold goes through the
	// upload session and lands byte-identical.
	big := filepath.Join(dir, "big.bin")
	bigBytes := bytes.Repeat([]byte{0xAB}, (16<<20)+512)
	if err := os.WriteFile(big, bigBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	run("put", big, "files/e2e/big.bin")
	if got := fake.files["files/e2e/big.bin"]; len(got) != len(bigBytes) || !bytes.Equal(got, bigBytes) {
		t.Fatalf("multipart content mismatch: got %d bytes, want %d", len(got), len(bigBytes))
	}
	if fake.multipartSessions != 1 {
		t.Fatalf("expected 1 multipart session, saw %d", fake.multipartSessions)
	}
}

// fakeDrive is a minimal stateful Drive: enough of the file plane for the
// roundtrip above.
type fakeDrive struct {
	t    *testing.T
	base string

	mu                sync.Mutex
	files             map[string][]byte
	trash             map[string][]byte
	parts             map[string][][]byte // upload id -> part bodies
	partCounts        map[string]int
	uploadPaths       map[string]string
	multipartSessions int
	nextUpload        int
}

func newFakeDrive(t *testing.T) *fakeDrive {
	return &fakeDrive{
		t:           t,
		files:       map[string][]byte{},
		trash:       map[string][]byte{},
		parts:       map[string][][]byte{},
		partCounts:  map[string]int{},
		uploadPaths: map[string]string{},
	}
}

func (f *fakeDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/blob/"):
		_, _ = w.Write(f.files[strings.TrimPrefix(p, "/blob/")])
	case strings.HasPrefix(p, "/part/"):
		f.handlePart(w, r)
	case p == "/api/v1/uploads":
		f.handleCreateUpload(w, r)
	case strings.HasPrefix(p, "/api/v1/uploads/") && strings.HasSuffix(p, "/complete"):
		f.handleCompleteUpload(w, r)
	case p == "/api/v1/trash" && r.Method == http.MethodGet:
		var entries []map[string]any
		for path, b := range f.trash {
			entries = append(entries, map[string]any{"path": path, "size": len(b), "created_by": "u", "deleted_at": "2026-07-12T00:00:00Z"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
	case p == "/api/v1/trash/restore":
		var req struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if b, ok := f.trash[req.Path]; ok {
			f.files[req.Path] = b
			delete(f.trash, req.Path)
			_ = json.NewEncoder(w).Encode(map[string]string{"path": req.Path, "status": "restored"})
			return
		}
		f.err(w, 404, "not in trash")
	case strings.HasPrefix(p, "/api/v1/files/me/"):
		f.handleFile(w, r, strings.TrimPrefix(p, "/api/v1/files/me/"))
	default:
		f.err(w, 404, "unexpected route "+r.Method+" "+p)
	}
}

func (f *fakeDrive) handleFile(w http.ResponseWriter, r *http.Request, path string) {
	q := r.URL.Query()
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		status := http.StatusCreated
		if _, ok := f.files[path]; ok {
			status = http.StatusOK
		}
		f.files[path] = b
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "size": len(b), "checksum": "ck"})
	case http.MethodGet:
		if _, ok := q["list"]; ok {
			var entries []map[string]any
			for fp, b := range f.files {
				if strings.HasPrefix(fp, path+"/") || fp == path {
					entries = append(entries, map[string]any{"path": fp, "size": len(b)})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
			return
		}
		if _, ok := f.files[path]; !ok {
			f.err(w, 404, "not found")
			return
		}
		http.Redirect(w, r, f.base+"/blob/"+path, http.StatusFound)
	case http.MethodDelete:
		b, ok := f.files[path]
		if !ok {
			f.err(w, 404, "not found")
			return
		}
		delete(f.files, path)
		if q.Get("permanent") != "true" {
			f.trash[path] = b
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		f.err(w, 405, "method")
	}
}

func (f *fakeDrive) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	const partSize = 16 << 20
	count := int((req.Size + partSize - 1) / partSize)
	f.nextUpload++
	f.multipartSessions++
	id := fmt.Sprintf("up-%d", f.nextUpload)
	f.parts[id] = make([][]byte, count)
	f.partCounts[id] = count
	f.uploadPaths[id] = req.Path
	urls := make([]string, count)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/part/%s/%d", f.base, id, i+1)
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"upload_id": id, "owner": "me", "path": req.Path,
		"part_size": partSize, "part_count": count, "part_urls": urls,
	})
}

func (f *fakeDrive) handlePart(w http.ResponseWriter, r *http.Request) {
	seg := strings.Split(strings.TrimPrefix(r.URL.Path, "/part/"), "/")
	if len(seg) != 2 {
		f.err(w, 400, "bad part url")
		return
	}
	id := seg[0]
	var n int
	_, _ = fmt.Sscanf(seg[1], "%d", &n)
	if r.Header.Get("Authorization") != "" {
		f.t.Error("bearer leaked to presigned part URL")
	}
	b, _ := io.ReadAll(r.Body)
	f.parts[id][n-1] = b
	w.Header().Set("ETag", fmt.Sprintf(`"etag-%s-%d"`, id, n))
}

func (f *fakeDrive) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/uploads/"), "/complete")
	var req struct {
		Parts []struct {
			N    int    `json:"n"`
			Etag string `json:"etag"`
		} `json:"parts"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if len(req.Parts) != f.partCounts[id] {
		f.err(w, 400, "wrong part count")
		return
	}
	var buf bytes.Buffer
	for _, b := range f.parts[id] {
		buf.Write(b)
	}
	path := f.uploadPaths[id]
	f.files[path] = buf.Bytes()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "size": buf.Len(), "checksum": "composite"})
}

func (f *fakeDrive) err(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
