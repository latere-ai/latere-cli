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

func TestDriveRmPurgeErrorE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"success", 200, `{"purged":1}`, ""},
		{"null receipt", 200, `null`, "purge outcome is unknown"},
		{"missing count", 200, `{}`, "purge outcome is unknown"},
		{"null count", 200, `{"purged":null}`, "purge outcome is unknown"},
		{"negative count", 200, `{"purged":-1}`, "purge outcome is unknown"},
		{"forbidden", 403, `{"error":"purge forbidden"}`, "purge forbidden"},
		{"server failure", 500, `{"error":"purge failed"}`, "purge failed"},
		{"invalid response", 200, `{"purged":`, "unexpected EOF"},
		{"extra response", 200, `{"purged":1} {}`, "multiple JSON values"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var live, trash atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				switch r.URL.Path {
				case "/api/v1/files/org/files/item":
					live.Add(1)
					if r.URL.Query().Get("permanent") != "true" {
						t.Error("missing permanent flag")
					}
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"error":"live lookup missed"}`)
				case "/api/v1/trash":
					trash.Add(1)
					if r.URL.Query().Get("owner") != "org" || r.URL.Query().Get("path") != "files/item" {
						t.Errorf("purge query=%s", r.URL.RawQuery)
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				default:
					t.Errorf("unexpected path=%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "drive", "rm", "files/item", "--permanent", "--owner", "org", "--drive-url", server.URL, "--token", "synthetic-token")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			err := command.Run()
			if tc.want == "" {
				if err != nil || diagnostic.String() != "Permanently deleted files/item\n" {
					t.Errorf("success: %v %q", err, diagnostic.String())
				}
			} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), tc.want) || strings.Contains(diagnostic.String(), "live lookup missed") || strings.Contains(diagnostic.String(), "Permanently deleted") {
				t.Errorf("error=%v stderr=%q, want %q", err, diagnostic.String(), tc.want)
			}
			if out.Len() != 0 || live.Load() != 1 || trash.Load() != 1 {
				t.Errorf("stdout=%q live=%d trash=%d", out.String(), live.Load(), trash.Load())
			}
		})
	}
}
