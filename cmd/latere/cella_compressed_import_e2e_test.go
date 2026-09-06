// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCellaCompressedTarImportE2E(t *testing.T) {
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
	for _, format := range []string{"tar", "tar.gz", "tar.bz2", "tar.xz", "v7.tar", "v7.tar.gz"} {
		for _, mode := range []string{"file", "stdin", "without extension"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					defer r.MultipartForm.RemoveAll()
					file, _, err := r.FormFile("tarball")
					if err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					defer file.Close()
					tr := tar.NewReader(file)
					hdr, err := tr.Next()
					if err != nil {
						http.Error(w, "server requires plain tar: "+err.Error(), 400)
						return
					}
					body, err := io.ReadAll(tr)
					if err != nil || hdr.Name != "nested/file.txt" || string(body) != "fixture contents\n" {
						t.Error("import changed archive contents")
						w.WriteHeader(400)
						return
					}
					if _, err := tr.Next(); err != io.EOF {
						t.Errorf("unexpected additional tar entry: %v", err)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"dest":"/workspace","bytes":17}`))
				}))
				defer server.Close()
				input := filepath.Join("..", "..", "internal", "commands", "testdata", "import", "payload."+format)
				if mode == "without extension" {
					data, err := os.ReadFile(input)
					if err != nil {
						t.Fatal(err)
					}
					input = filepath.Join(t.TempDir(), "archive")
					if err := os.WriteFile(input, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"cella", "import", "dev", "--api-url", server.URL, "--timeout", "5s"}
				if mode != "stdin" {
					args = append(args, "--input", input)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				if mode == "stdin" {
					file, err := os.Open(input)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					command.Stdin = file
				}
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				if out, err := command.CombinedOutput(); err != nil {
					t.Fatalf("import: %v\n%s", err, out)
				}
			})
		}
	}
}

func TestCellaImportRegularFileWithTarMagicE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	data := make([]byte, 512)
	copy(data[257:], "ustar")
	runCellaImportE2E(t, "ordinary.bin", []archiveEntry{{Name: "ordinary.bin", Body: string(data)}})
}

func TestCellaImportCompressedRegularFileE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	var raw bytes.Buffer
	writer := gzip.NewWriter(&raw)
	if _, err := writer.Write([]byte("ordinary data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	runCellaImportE2E(t, "compressed.bin", []archiveEntry{{Name: "compressed.bin", Body: raw.String()}})
}
