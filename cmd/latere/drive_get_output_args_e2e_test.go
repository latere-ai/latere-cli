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

func TestDriveGetOutputArgumentsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, destination string
		args              []string
	}{
		{"omitted", "report.txt", nil},
		{"file", "chosen.txt", []string{"--output", "chosen.txt"}},
		{"stdout", "-", []string{"-o", "-"}},
		{"space filename", " ", []string{"-o", " "}},
		{"empty long flag", "", []string{"--output="}},
		{"empty short flag", "", []string{"-o", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.destination == " " && runtime.GOOS == "windows" {
				t.Skip("Windows does not support a space-only filename")
			}
			dir := t.TempDir()
			files := []string{"report.txt", "chosen.txt"}
			if tc.destination == " " {
				files = append(files, " ")
			}
			for _, name := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("existing contents"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/files/me/files/report.txt" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, "downloaded contents")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			args := append([]string{"drive", "--drive-url", server.URL, "--token", "synthetic-token", "get", "files/report.txt"}, tc.args...)
			command := exec.CommandContext(ctx, binary, args...)
			command.Dir = dir
			command.Env = append(os.Environ(), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			err := command.Run()
			wantRequests := int32(1)
			if tc.destination == "" {
				wantRequests = 0
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "--output cannot be empty") || strings.Contains(diagnostic.String(), "Downloaded") {
					t.Errorf("empty output: error=%v stderr=%q", err, diagnostic.String())
				}
			} else if err != nil {
				t.Errorf("valid output: error=%v stderr=%q", err, diagnostic.String())
			}
			for _, name := range files {
				want := "existing contents"
				if tc.destination == name {
					want = "downloaded contents"
				}
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != want {
					t.Errorf("file %q: contents=%q want=%q error=%v", name, got, want, err)
				}
			}
			wantOutput := ""
			if tc.destination == "-" {
				wantOutput = "downloaded contents"
			}
			if out.String() != wantOutput || requests.Load() != wantRequests {
				t.Errorf("stdout=%q want=%q requests=%d want=%d", out.String(), wantOutput, requests.Load(), wantRequests)
			}
		})
	}
}
