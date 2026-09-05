//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
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
	"syscall"
	"testing"
	"time"
)

func TestCellaImportRejectsSpecialFilesE2E(t *testing.T) {
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
	for _, kind := range []string{"device", "device.tar", "device.zip", "pipe", "pipe.tar", "directory"} {
		t.Run(kind, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), kind)
			switch kind {
			case "device":
				source = os.DevNull
			case "device.tar", "device.zip":
				if err := os.Symlink(os.DevNull, source); err != nil {
					t.Fatal(err)
				}
			case "pipe", "pipe.tar":
				if err := syscall.Mkfifo(source, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"dest":"/workspace"}`))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "cella", "import", "dev", "--input", source, "--timeout", "100ms", "--api-url", server.URL)
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			out, err := command.CombinedOutput()
			if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "not a regular file") {
				t.Errorf("special input result = %v; output: %s", err, out)
			}
			if requests.Load() != 0 {
				t.Errorf("special input sent %d HTTP requests", requests.Load())
			}
		})
	}
}
