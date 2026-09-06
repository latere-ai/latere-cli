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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCellaExportOutputArgumentsE2E(t *testing.T) {
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
		for _, tc := range []struct {
			name, destination string
			args              []string
		}{
			{"omitted", "-", nil},
			{"stdout", "-", []string{"-o", "-"}},
			{"file", "archive.tar", []string{"--output", "archive.tar"}},
			{"space filename", " ", []string{"-o", " "}},
			{"empty long flag", "", []string{"--output="}},
			{"empty short flag", "", []string{"-o", ""}},
		} {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				if tc.destination == " " && runtime.GOOS == "windows" {
					t.Skip("Windows does not support a space-only filename")
				}
				dir := t.TempDir()
				filename := "archive.tar"
				if tc.destination == " " {
					filename = " "
				}
				dest := filepath.Join(dir, filename)
				if err := os.WriteFile(dest, []byte("previous"), 0600); err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/dev/files/export") {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					if r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Error("incorrect authorization")
					}
					_, _ = io.WriteString(w, "archive contents")
				}))
				defer server.Close()
				args := append([]string{prefix, "export", "dev", "--api-url", server.URL}, tc.args...)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Dir = dir
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				wantRequests := int32(1)
				wantOut, wantFile := "", "previous"
				if tc.destination == "" {
					wantRequests = 0
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("empty output: error=%v", err)
					}
					if !strings.Contains(diagnostic.String(), "--output cannot be empty") {
						t.Errorf("stderr=%q", diagnostic.String())
					}
				} else {
					if err != nil {
						t.Errorf("valid output: error=%v stderr=%q", err, diagnostic.String())
					}
					if tc.destination == "-" {
						wantOut = "archive contents"
					} else {
						wantFile = "archive contents"
					}
				}
				if requests.Load() != wantRequests {
					t.Errorf("requests=%d want=%d", requests.Load(), wantRequests)
				}
				if out.String() != wantOut {
					t.Errorf("stdout=%q want=%q", out.String(), wantOut)
				}
				got, err := os.ReadFile(dest)
				if err != nil || string(got) != wantFile {
					t.Errorf("file=%q want=%q error=%v", got, wantFile, err)
				}
			})
		}
	}
}
