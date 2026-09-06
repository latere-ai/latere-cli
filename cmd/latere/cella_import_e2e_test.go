// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestCellaImportRegularFileE2E(t *testing.T) {
	runCellaImportE2E(t, "64ceff9a-01f2-4b3c-9b94-9d4c59435c15.jsonl", []archiveEntry{
		{Name: "64ceff9a-01f2-4b3c-9b94-9d4c59435c15.jsonl", Body: "{\"ok\":true}\n"},
	})
}

func TestCellaImportRegularFileWithZipLikePrefixE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	for _, third := range []byte{0x03, 0x05, 0x07} {
		for _, fourth := range []byte{0x04, 0x06, 0x08} {
			if fourth == third+1 {
				continue
			}
			t.Run(fmt.Sprintf("PK%02x%02x", third, fourth), func(t *testing.T) {
				runCellaImportE2E(t, "ordinary.bin", []archiveEntry{
					{Name: "ordinary.bin", Body: string([]byte{'P', 'K', third, fourth}) + "ordinary file contents"},
				})
			})
		}
	}
}

func TestCellaImportZipE2E(t *testing.T) {
	runCellaImportE2E(t, "payload.zip", []archiveEntry{
		{Name: "data/one.jsonl", Body: "{\"one\":true}\n"},
		{Name: "two.txt", Body: "two\n"},
	})
}

func TestCellaImportZipDirectoriesE2E(t *testing.T) {
	runCellaImportE2E(t, "directories.zip", []archiveEntry{
		{Name: "empty/"},
		{Name: "data/"},
		{Name: "data/nested/"},
		{Name: "data/nested/one.txt", Body: "one\n"},
	})
}

type archiveEntry struct {
	Name string
	Body string
}

func runCellaImportE2E(t *testing.T, inputName string, wantEntries []archiveEntry) {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	if err := api.SaveToken(tokenPath, api.Token{AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}

	inputPath := filepath.Join(dir, inputName)
	if strings.HasSuffix(inputName, ".zip") {
		if err := writeZipFixture(inputPath, wantEntries); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(inputPath, []byte(wantEntries[0].Body), 0o600); err != nil {
		t.Fatal(err)
	}

	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/v1/sandboxes/lively-ibis-5ev/files/import"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
			t.Fatalf("authorization = %q, want %q", got, want)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if got, want := r.FormValue("dest"), "/workspace"; got != want {
			t.Fatalf("dest = %q, want %q", got, want)
		}
		file, receipt, err := r.FormFile("tarball")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		tr := tar.NewReader(file)
		hdr, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range wantEntries {
			if i > 0 {
				hdr, err = tr.Next()
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := hdr.Name; got != want.Name {
				t.Fatalf("tar entry %d = %q, want %q", i, got, want.Name)
			}
			if strings.HasSuffix(want.Name, "/") && hdr.Typeflag != tar.TypeDir {
				t.Fatalf("tar entry %q has type %d, want directory", hdr.Name, hdr.Typeflag)
			}
			gotBody, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotBody) != want.Body {
				t.Fatalf("tar body %d = %q, want %q", i, gotBody, want.Body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"imported": filepath.Base(inputPath),
			"bytes":    receipt.Size,
			"dest":     "/workspace",
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "cella", "import", "lively-ibis-5ev",
		"--api-url", srv.URL,
		"--input", inputPath,
		"--dest", "/workspace",
		"--timeout", "0",
	)
	cmd.Env = append(os.Environ(),
		"LATERE_TOKEN_FILE="+tokenPath,
		"LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"),
		"LATERE_NO_UPDATE_CHECK=1",
		"OTEL_SDK_DISABLED=true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("latere import failed: %v\n%s", err, out)
	}
	if !sawRequest {
		t.Fatal("server did not receive import request")
	}
	if !strings.Contains(string(out), `"dest": "/workspace"`) {
		t.Fatalf("output missing import response: %s", out)
	}
}

func writeZipFixture(path string, entries []archiveEntry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	for _, entry := range entries {
		w, err := zw.Create(entry.Name)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if _, err := io.WriteString(w, entry.Body); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}
