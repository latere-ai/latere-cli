// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestDrivePaginationCyclesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, verb := range [][]string{{"ls"}, {"ls", "--trashed"}, {"history", "files/item"}, {"shares"}, {"shares", "--inbox"}} {
		for _, tc := range []struct {
			name  string
			next  []string
			valid bool
		}{
			{"valid with empty page", []string{"a", "b", ""}, true},
			{"same cursor", []string{"a", "a"}, false},
			{"long cycle", []string{"a", "b", "a"}, false},
		} {
			for _, format := range []string{"text", "json"} {
				t.Run(strings.Join(verb, " ")+"/"+tc.name+"/"+format, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						i := int(requests.Add(1)) - 1
						if i >= len(tc.next) {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						if r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Error("missing synthetic auth")
						}
						entries := []map[string]any{}
						if i != 1 {
							entries = append(entries, map[string]any{"path": "files/item", "id": "share", "version_no": i + 1})
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries, "next_cursor": tc.next[i]})
					}))
					defer server.Close()
					args := append([]string{"drive", "--drive-url", server.URL, "--token", "synthetic-token"}, verb...)
					if format == "json" {
						args = append(args, "--json")
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					if tc.valid {
						if err != nil || diagnostic.Len() != 0 {
							t.Errorf("valid pagination: err=%v stderr=%q", err, diagnostic.String())
						}
						if format == "json" {
							var entries []any
							if json.Unmarshal(out.Bytes(), &entries) != nil || len(entries) != 2 {
								t.Errorf("JSON output=%q", out.String())
							}
						} else if strings.Count(out.String(), "\n") != 2 {
							t.Errorf("text output=%q", out.String())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "pagination returned a repeated cursor") || out.Len() != 0 {
						t.Errorf("cycle: err=%v output=%q stderr=%q", err, out.String(), diagnostic.String())
					}
					if requests.Load() != int32(len(tc.next)) {
						t.Errorf("requests=%d, want %d", requests.Load(), len(tc.next))
					}
				})
			}
		}
	}
}
