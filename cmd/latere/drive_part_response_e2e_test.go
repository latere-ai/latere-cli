// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
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

func TestDriveMultipartRequiresCompletePartResponseE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary, source := filepath.Join(root, "latere"), filepath.Join(root, "source")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	const size = 17 << 20
	if err := os.WriteFile(source, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(source, size); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"empty", "body", "no content", "short content length", "interrupted chunks", "HTTP error"} {
		t.Run(state, func(t *testing.T) {
			wantError := ""
			switch state {
			case "short content length", "interrupted chunks":
				wantError = "unexpected EOF"
			case "HTTP error":
				wantError = "object store HTTP 503"
			}
			var puts, completes, aborts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/part" && r.Header.Get("Authorization") != "Bearer test-drive" {
					t.Error("Drive control request lost authentication")
				}
				switch r.URL.Path {
				case "/api/v1/uploads":
					_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "test-upload", "path": "files/test", "part_size": size, "part_count": 1, "part_urls": []string{"http://" + r.Host + "/part"}})
				case "/part":
					puts.Add(1)
					n, err := io.Copy(io.Discard, r.Body)
					if err != nil || n != size || r.Method != http.MethodPut || r.Header.Get("Authorization") != "" {
						t.Errorf("invalid part upload: bytes=%d, err=%v", n, err)
					}
					w.Header().Set("ETag", `"test-etag"`)
					switch state {
					case "empty":
						return
					case "no content":
						w.WriteHeader(http.StatusNoContent)
						return
					case "short content length", "HTTP error":
						w.Header().Set("Content-Length", "20")
						if state == "HTTP error" {
							w.WriteHeader(http.StatusServiceUnavailable)
						}
					}
					_, _ = w.Write([]byte("part response"))
					if state == "interrupted chunks" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				case "/api/v1/uploads/test-upload/complete":
					completes.Add(1)
					var body struct {
						Parts []struct {
							N    int    `json:"n"`
							ETag string `json:"etag"`
						} `json:"parts"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Parts) != 1 || body.Parts[0].N != 1 || body.Parts[0].ETag != "test-etag" {
						t.Error("completion lost the uploaded part's ETag")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"path": "files/test", "size": size})
				case "/api/v1/uploads/test-upload":
					aborts.Add(1)
					if r.Method != http.MethodDelete {
						t.Errorf("unexpected cleanup method: %s", r.Method)
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "drive", "put", source, "files/test", "--drive-url", server.URL, "--token", "test-drive")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			wantCompletes, wantAborts := int32(1), int32(0)
			if wantError != "" {
				wantCompletes, wantAborts = 0, 1
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), wantError) || strings.Contains(string(out), "Uploaded") {
					t.Errorf("invalid part response reported success: %v: %s", err, out)
				}
			} else if err != nil || !strings.Contains(string(out), "Uploaded") {
				t.Errorf("valid upload failed: %v: %s", err, out)
			}
			if puts.Load() != 1 || completes.Load() != wantCompletes || aborts.Load() != wantAborts {
				t.Errorf("part/complete/abort calls=%d/%d/%d, want 1/%d/%d", puts.Load(), completes.Load(), aborts.Load(), wantCompletes, wantAborts)
			}
		})
	}
}
