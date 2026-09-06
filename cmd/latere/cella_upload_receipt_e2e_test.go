// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
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

func TestCellaUploadReceiptE2E(t *testing.T) {
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
	for _, tc := range []struct {
		name, contents, receipt string
		valid                   bool
	}{
		{"complete", "content", `{"files":1,"bytes":7,"dest":"/workspace"}`, true},
		{"empty file", "", `{"files":1,"bytes":0,"dest":"/workspace"}`, true},
		{"missing file", "content", `{"files":0,"bytes":0,"dest":"/workspace"}`, false},
		{"short bytes", "content", `{"files":1,"bytes":6,"dest":"/workspace"}`, false},
		{"extra bytes", "content", `{"files":1,"bytes":8,"dest":"/workspace"}`, false},
		{"missing receipt", "content", `{}`, false},
		{"null receipt", "content", `null`, false},
		{"no content", "content", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(source, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/dev/files/upload" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				parts, err := r.MultipartReader()
				if err != nil {
					t.Error(err)
					return
				}
				part, err := parts.NextPart()
				if err != nil {
					t.Error(err)
					return
				}
				data, err := io.ReadAll(part)
				if err != nil || string(data) != tc.contents || part.FormName() != "file" {
					t.Errorf("uploaded data = %q, %v; name=%q", data, err, part.FormName())
				}
				if _, err := parts.NextPart(); !errors.Is(err, io.EOF) {
					t.Errorf("multipart not complete: %v", err)
				}
				if tc.receipt == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_, _ = w.Write([]byte(tc.receipt))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "cella", "upload", "dev", source, "--api-url", server.URL)
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if tc.valid {
				if err != nil || !strings.Contains(stdout.String(), "uploaded 1 files") {
					t.Errorf("valid receipt = %v, stdout=%q, stderr=%q", err, stdout.String(), stderr.String())
				}
			} else {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "upload receipt") {
					t.Errorf("invalid receipt returned %v: %s", err, stderr.String())
				}
				if stdout.Len() != 0 {
					t.Errorf("invalid receipt printed success: %q", stdout.String())
				}
			}
			if requests.Load() != 1 {
				t.Errorf("upload requests=%d, want 1", requests.Load())
			}
		})
	}
}
