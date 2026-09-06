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

func TestDriveDeleteRequiresCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []struct {
		name, path, query, want string
		args                    []string
	}{
		{"trash", "/api/v1/files/me/files/item", "", "Trashed files/item (restore with `latere drive restore files/item`)\n", []string{"rm", "files/item"}},
		{"permanent", "/api/v1/files/me/files/item", "permanent=true", "Permanently deleted files/item\n", []string{"rm", "files/item", "--permanent"}},
		{"version", "/api/v1/files/me/files/item", "version=2", "Pruned version 2 of files/item\n", []string{"rm", "files/item", "--version", "2"}},
		{"revoke", "/api/v1/shares/share-1", "", "Revoked share share-1\n", []string{"unshare", "share-1"}},
	} {
		for _, status := range []int{200, 202, 204, 403} {
			t.Run(fmt.Sprintf("%s/%d", operation.name, status), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodDelete || r.URL.Path != operation.path || r.URL.RawQuery != operation.query || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					w.WriteHeader(status)
					switch status {
					case 202:
						_, _ = io.WriteString(w, `{"status":"pending"}`)
					case 403:
						_, _ = io.WriteString(w, `{"error":"denied"}`)
					case 200:
						_, _ = io.WriteString(w, `{}`)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				args := append([]string{"drive", "--drive-url", server.URL, "--token", "synthetic-token"}, operation.args...)
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				if status == 200 || status == 204 {
					if err != nil || diagnostic.String() != operation.want {
						t.Errorf("completed: error=%v stderr=%q", err, diagnostic.String())
					}
				} else {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || strings.Contains(diagnostic.String(), operation.want) {
						t.Errorf("unconfirmed: error=%v stderr=%q", err, diagnostic.String())
					}
					want := "denied"
					if status == 202 {
						want = "outcome is unknown"
					}
					if !strings.Contains(diagnostic.String(), want) {
						t.Errorf("missing %q in stderr=%q", want, diagnostic.String())
					}
				}
				if out.Len() != 0 || requests.Load() != 1 {
					t.Errorf("stdout=%q requests=%d", out.String(), requests.Load())
				}
			})
		}
	}
}
