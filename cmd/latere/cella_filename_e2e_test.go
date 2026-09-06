//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestCellaMultipartFilenamesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	tokenPath := filepath.Join(root, "token.json")
	if err := os.WriteFile(tokenPath, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "file", Mode: 0600, Size: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"upload", "import"} {
		for _, name := range []string{"line\nbreak.tar", "carriage\rreturn.tar", "quote\"back\\slash.tar", "雪 + %0A.tar", "invalid-\xff\xfe.tar"} {
			t.Run(action+"/"+name, func(t *testing.T) {
				source := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(source, archive.Bytes(), 0600); err != nil {
					if errors.Is(err, syscall.EILSEQ) {
						t.Skip("filesystem rejects invalid UTF-8 filenames")
					}
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					parts, err := r.MultipartReader()
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					var files int
					for {
						part, err := parts.NextPart()
						if errors.Is(err, io.EOF) {
							break
						}
						if err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						data, err := io.ReadAll(part)
						if err != nil {
							t.Error(err)
						}
						if part.FileName() == "" {
							continue
						}
						files++
						field := name
						if action == "import" {
							field = "tarball"
						}
						if part.FormName() != field || part.FileName() != name || !bytes.Equal(data, archive.Bytes()) {
							t.Errorf("part = (%q, %q, %d bytes), want (%q, %q, %d bytes)", part.FormName(), part.FileName(), len(data), field, name, archive.Len())
						}
					}
					if files != 1 {
						t.Errorf("file parts = %d, want 1", files)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"dest": "/workspace", "files": 1, "bytes": archive.Len(), "imported": name})
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				args := []string{"cella", action, "dev"}
				if action == "import" {
					args = append(args, "--input")
				}
				args = append(args, source, "--api-url", server.URL)
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				if out, err := command.CombinedOutput(); err != nil {
					t.Errorf("%s: %v\n%s", action, err, out)
				}
				if requests.Load() != 1 {
					t.Errorf("requests = %d, want 1", requests.Load())
				}
			})
		}
	}
}
