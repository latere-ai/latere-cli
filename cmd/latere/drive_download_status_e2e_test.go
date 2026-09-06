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

func TestDriveDownloadRequiresCompleteStatusE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, redirect := range []bool{false, true} {
		for _, dest := range []string{"file", "stdout"} {
			for _, tc := range []struct {
				name   string
				status int
				body   string
			}{
				{"complete", 200, "file bytes"},
				{"empty file", 200, ""},
				{"accepted", 202, `{"status":"pending"}`},
				{"no content", 204, ""},
				{"partial content", 206, "fil"},
			} {
				t.Run(fmt.Sprintf("%s/%s/redirect=%t", tc.name, dest, redirect), func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
							t.Errorf("unexpected request: %s Range=%q", r.Method, r.Header.Get("Range"))
						}
						if r.URL.Path != "/object" {
							if r.Header.Get("Authorization") != "Bearer synthetic-token" {
								t.Error("missing synthetic auth")
							}
							if redirect {
								http.Redirect(w, r, "/object", http.StatusFound)
								return
							}
						} else if r.Header.Get("Authorization") != "" {
							t.Error("bearer forwarded to object URL")
						}
						if tc.status == 206 {
							w.Header().Set("Content-Range", "bytes 0-2/10")
						}
						w.WriteHeader(tc.status)
						_, _ = io.WriteString(w, tc.body)
					}))
					defer server.Close()
					dir := t.TempDir()
					output := filepath.Join(dir, "output")
					const previous = "previous file data"
					if err := os.WriteFile(output, []byte(previous), 0640); err != nil {
						t.Fatal(err)
					}
					target := output
					if dest == "stdout" {
						target = "-"
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, "drive", "get", "files/item", "-o", target, "--drive-url", server.URL, "--token", "synthetic-token")
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					if tc.status == 200 {
						if err != nil {
							t.Errorf("complete download: err=%v stderr=%q", err, diagnostic.String())
						}
						wantOut := ""
						if dest == "stdout" {
							wantOut = tc.body
						}
						if out.String() != wantOut {
							t.Errorf("stdout=%q, want %q", out.String(), wantOut)
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "complete download") || strings.Contains(diagnostic.String(), "Downloaded") || out.Len() != 0 {
						t.Errorf("incomplete download: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
					}
					wantFile := previous
					if dest == "file" && tc.status == 200 {
						wantFile = tc.body
					}
					if data, err := os.ReadFile(output); err != nil || string(data) != wantFile {
						t.Errorf("destination=%q error=%v, want %q", data, err, wantFile)
					}
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 1 {
						t.Errorf("download left scratch files: %v %v", entries, err)
					}
					wantRequests := int32(1)
					if redirect {
						wantRequests = 2
					}
					if requests.Load() != wantRequests {
						t.Errorf("requests=%d, want %d", requests.Load(), wantRequests)
					}
				})
			}
		}
	}
}
