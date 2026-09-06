// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCellaDownloadStatusE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	token := filepath.Join(root, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, mode := range []string{"export file", "export stdout", "cat"} {
			for _, tc := range []struct {
				name   string
				status int
				body   string
			}{
				{"complete", 200, "complete contents"},
				{"empty", 200, ""},
				{"pending", 202, `{"pending":true}`},
				{"no content", 204, ""},
				{"partial", 206, "partial"},
			} {
				t.Run(prefix+"/"+mode+"/"+tc.name, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Header.Get("Authorization") != "Bearer synthetic-token" || r.Header.Get("Range") != "" {
							t.Error("incorrect authorization or range")
						}
						if mode == "cat" {
							if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/files") || r.URL.Query().Get("path") != "/workspace/file" || r.URL.Query().Get("raw") != "true" {
								t.Errorf("cat request=%s %s", r.Method, r.URL)
							}
						} else if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/files/export") {
							t.Errorf("export request=%s %s", r.Method, r.URL)
						}
						if tc.status == 206 {
							w.Header().Set("Content-Range", "bytes 0-6/100")
						}
						w.WriteHeader(tc.status)
						if tc.status != 204 {
							_, _ = io.WriteString(w, tc.body)
						}
					}))
					defer server.Close()
					dir := t.TempDir()
					dest := filepath.Join(dir, "archive.tar")
					if err := os.WriteFile(dest, []byte("previous"), 0600); err != nil {
						t.Fatal(err)
					}
					args := []string{prefix, "export", "dev", "--api-url", server.URL}
					switch mode {
					case "export file":
						args = append(args, "-o", dest)
					case "cat":
						args = []string{prefix, "cat", "dev", "/workspace/file", "--api-url", server.URL}
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					if tc.status == 200 {
						if err != nil || diagnostic.Len() != 0 {
							t.Errorf("valid download: %v %q", err, diagnostic.String())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "complete download") || !strings.Contains(diagnostic.String(), fmt.Sprint(tc.status)) {
						t.Errorf("invalid download: %v %q", err, diagnostic.String())
					}
					wantOut, wantFile := "", "previous"
					if tc.status == 200 {
						if mode == "export file" {
							wantFile = tc.body
						} else {
							wantOut = tc.body
						}
					}
					data, readErr := os.ReadFile(dest)
					if readErr != nil || string(data) != wantFile || out.String() != wantOut {
						t.Errorf("file=%q stdout=%q read=%v; want file=%q stdout=%q", data, out.String(), readErr, wantFile, wantOut)
					}
					entries, readErr := os.ReadDir(dir)
					if readErr != nil || len(entries) != 1 {
						t.Errorf("leftover files: %v %v", entries, readErr)
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d", requests.Load())
					}
				})
			}
		}
	}
}
