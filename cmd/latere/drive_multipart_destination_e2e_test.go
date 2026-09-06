// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDriveMultipartDestinationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary, source := filepath.Join(root, "latere"), filepath.Join(root, "source")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	const size = (16 << 20) + 1
	if err := os.WriteFile(source, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(source, size); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want, returned string
		valid                bool
	}{
		{"matching", "files/item", "files/item", true},
		{"special characters", "files/ü ?#%/item", "files/ü ?#%/item", true},
		{"missing", "files/item", "", false},
		{"wrong", "files/item", "files/other", false},
	} {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				var created, parts, completed, aborted atomic.Int32
				var uploaded atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/uploads":
						created.Add(1)
						var req struct{ Path string }
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path != tc.want {
							t.Errorf("requested path=%q error=%v", req.Path, err)
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "upload", "path": tc.returned, "part_size": 16 << 20, "part_count": 2, "part_urls": []string{"http://" + r.Host + "/part", "http://" + r.Host + "/part"}})
					case "/part":
						parts.Add(1)
						n, err := io.Copy(io.Discard, r.Body)
						if err != nil {
							t.Error(err)
						}
						uploaded.Add(n)
						w.Header().Set("ETag", "part")
					case "/api/v1/uploads/upload/complete":
						completed.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]any{"path": tc.returned, "size": size})
					case "/api/v1/uploads/upload":
						if r.Method != http.MethodDelete {
							t.Errorf("cleanup method=%s", r.Method)
						}
						aborted.Add(1)
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Errorf("unexpected endpoint %s", r.URL)
					}
				}))
				defer server.Close()
				args := []string{"drive", "put", source, tc.want, "--drive-url", server.URL, "--token", "synthetic-token"}
				if format == "json" {
					args = append(args, "--json")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				wantParts, wantCompleted, wantAborted := int32(0), int32(0), int32(1)
				wantBytes := int64(0)
				if tc.valid {
					wantParts, wantCompleted, wantAborted, wantBytes = 2, 1, 0, size
					if err != nil || !strings.Contains(out.String()+diagnostic.String(), tc.want) {
						t.Errorf("valid session: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "upload session destination") || strings.Contains(diagnostic.String(), "Uploaded") || out.Len() != 0 {
					t.Errorf("invalid session: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if created.Load() != 1 || parts.Load() != wantParts || completed.Load() != wantCompleted || aborted.Load() != wantAborted || uploaded.Load() != wantBytes {
					t.Errorf("created=%d parts=%d complete=%d abort=%d bytes=%d, want 1/%d/%d/%d/%d", created.Load(), parts.Load(), completed.Load(), aborted.Load(), uploaded.Load(), wantParts, wantCompleted, wantAborted, wantBytes)
				}
			})
		}
	}
}
