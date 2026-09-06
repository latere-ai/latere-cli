// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDriveUploadReceiptE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"single", "empty", "multipart"} {
		size := int64(7)
		switch mode {
		case "empty":
			size = 0
		case "multipart":
			size = (16 << 20) + 1
		}
		source := filepath.Join(t.TempDir(), "source")
		file, err := os.Create(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(size); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		valid := fmt.Sprintf(`{"path":"files/item","size":%d,"checksum":"opaque"}`, size)
		for _, tc := range []struct {
			name, body string
			valid      bool
		}{
			{"valid", valid, true},
			{"null", "null", false},
			{"wrong path", strings.Replace(valid, "files/item", "files/other", 1), false},
			{"wrong size", strings.Replace(valid, fmt.Sprintf(`"size":%d`, size), fmt.Sprintf(`"size":%d`, size+1), 1), false},
			{"missing size", strings.Replace(valid, fmt.Sprintf(`"size":%d,`, size), "", 1), false},
		} {
			for _, format := range []string{"text", "json"} {
				t.Run(mode+"/"+tc.name+"/"+format, func(t *testing.T) {
					var receipts, parts, aborts atomic.Int32
					var uploaded atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch {
						case r.Method == http.MethodDelete:
							aborts.Add(1)
							w.WriteHeader(http.StatusNoContent)
						case r.URL.Path == "/api/v1/uploads":
							_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "upload", "part_size": 16 << 20, "part_count": 2, "part_urls": []string{"http://" + r.Host + "/part", "http://" + r.Host + "/part"}})
						case r.URL.Path == "/part":
							parts.Add(1)
							n, err := io.Copy(io.Discard, r.Body)
							if err != nil {
								t.Error(err)
							}
							uploaded.Add(n)
							w.Header().Set("ETag", "part")
						default:
							receipts.Add(1)
							n, err := io.Copy(io.Discard, r.Body)
							if err != nil {
								t.Error(err)
							}
							if r.Method == http.MethodPut {
								uploaded.Add(n)
							}
							_, _ = io.WriteString(w, tc.body)
						}
					}))
					defer server.Close()
					args := []string{"drive", "put", source, "files/item", "--drive-url", server.URL, "--token", "synthetic-token"}
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
					if tc.valid {
						if err != nil || !strings.Contains(out.String()+diagnostic.String(), "files/item") {
							t.Errorf("valid receipt: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "upload receipt") || strings.Contains(diagnostic.String(), "Uploaded") || out.Len() != 0 {
						t.Errorf("invalid receipt: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
					}
					wantParts, wantAborts := int32(0), int32(0)
					if mode == "multipart" {
						wantParts = 2
						if !tc.valid {
							wantAborts = 1
						}
					}
					if receipts.Load() != 1 || parts.Load() != wantParts || aborts.Load() != wantAborts || uploaded.Load() != size {
						t.Errorf("receipts=%d parts=%d aborts=%d bytes=%d, want 1/%d/%d/%d", receipts.Load(), parts.Load(), aborts.Load(), uploaded.Load(), wantParts, wantAborts, size)
					}
				})
			}
		}
	}
}
