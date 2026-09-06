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
	"testing"
	"time"
)

func TestCellaMultipartRequiresCompleteUploadE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	source := filepath.Join(root, "large.bin")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(root, "token.json")
	if err := os.WriteFile(tokenPath, []byte(`{"access_token":"test-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"upload", "import", "import stdin"} {
		for _, early := range []bool{false, true} {
			if verb == "import stdin" && !early {
				continue // The pipe intentionally stays open without supplying data.
			}
			name := "complete"
			if early {
				name = "early success"
			}
			t.Run(verb+"/"+name, func(t *testing.T) {
				const reply = `{"files":1,"bytes":16777216,"dest":"/workspace","imported":"ok"}`
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if early {
						conn, buffer, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.Close() }()
						_, _ = fmt.Fprintf(buffer, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(reply), reply)
						_ = buffer.Flush()
						return
					}
					parts, err := r.MultipartReader()
					if err != nil {
						t.Error(err)
						return
					}
					var total int64
					for {
						part, err := parts.NextPart()
						if errors.Is(err, io.EOF) {
							break
						}
						if err != nil {
							t.Error(err)
							return
						}
						n, err := io.Copy(io.Discard, part)
						if err != nil {
							t.Error(err)
							return
						}
						total += n
					}
					if total < 16<<20 {
						t.Errorf("incomplete payload: %d bytes", total)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"files": 1, "bytes": total, "dest": "/workspace", "imported": filepath.Base(source)})
				}))
				defer server.Close()
				args := []string{"cella", verb, "dev"}
				switch verb {
				case "import stdin":
					args[1] = "import"
				case "import":
					args = append(args, "--input")
				}
				if verb != "import stdin" {
					args = append(args, source)
				}
				args = append(args, "--api-url", server.URL)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				if verb == "import stdin" {
					reader, writer, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = reader.Close() }()
					defer func() { _ = writer.Close() }()
					command.Stdin = reader
				}
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"),
					"XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err = command.Run()
				if early {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || out.Len() != 0 {
						t.Errorf("incomplete upload returned %v: stdout=%s stderr=%s", err, out.String(), diagnostic.String())
					}
				} else if err != nil {
					t.Errorf("complete upload failed: %v: %s", err, diagnostic.String())
				}
			})
		}
	}
}
