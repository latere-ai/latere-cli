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
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestCellaUploadValidatesSourcesE2E(t *testing.T) {
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
	for _, kind := range []string{"device", "pipe", "nested device", "nested pipe", "files"} {
		t.Run(kind, func(t *testing.T) {
			source := os.DevNull
			dir := filepath.Join(t.TempDir(), "dist")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "pipe":
				source = filepath.Join(t.TempDir(), "pipe")
				if err := syscall.Mkfifo(source, 0600); err != nil {
					t.Fatal(err)
				}
			case "nested pipe":
				source = dir
				if err := syscall.Mkfifo(filepath.Join(dir, "z-pipe"), 0600); err != nil {
					t.Fatal(err)
				}
			case "nested device":
				source = dir
				if err := os.Symlink(os.DevNull, filepath.Join(dir, "z-device")); err != nil {
					t.Fatal(err)
				}
			case "files":
				source = dir
				if err := os.Symlink("a.txt", filepath.Join(dir, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if kind != "files" {
					_, _ = io.Copy(io.Discard, r.Body)
					_, _ = w.Write([]byte(`{"files":1,"bytes":0}`))
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/dev/files/upload" {
					t.Error("unexpected upload request")
				}
				parts, err := r.MultipartReader()
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				got := map[string]string{}
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
					got[part.FormName()] = string(data)
				}
				want := map[string]string{"dest": "/workspace", "dist/a.txt": "content", "dist/empty": "", "dist/alias": "content"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("uploaded contents = %v, want %v", got, want)
				}
				_, _ = w.Write([]byte(`{"dest":"/workspace","files":3,"bytes":14}`))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			uploadTimeout := "200ms"
			if kind == "files" {
				uploadTimeout = "5s"
			}
			command := exec.CommandContext(ctx, binary, "cella", "upload", "dev", source, "--dest", "/workspace", "--timeout", uploadTimeout, "--api-url", server.URL)
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			out, err := command.CombinedOutput()
			if kind == "files" {
				if err != nil {
					t.Fatalf("upload: %v\n%s", err, out)
				}
				if requests.Load() != 1 {
					t.Errorf("upload requests = %d, want 1", requests.Load())
				}
			} else {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "not a regular file") {
					t.Errorf("invalid source result = %v; output: %s", err, out)
				}
				if requests.Load() != 0 {
					t.Errorf("invalid source sent %d requests", requests.Load())
				}
			}
		})
	}
}
