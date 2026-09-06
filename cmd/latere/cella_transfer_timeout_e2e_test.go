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

func TestCellaTransferTimeoutE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	tokenPath, source := filepath.Join(root, "token.json"), filepath.Join(root, "file")
	for path, data := range map[string]string{tokenPath: `{"access_token":"test-token"}`, source: "content"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, verb := range []string{"upload", "import"} {
		for _, duration := range []string{"0", "5s", "-1ns", "-1s"} {
			t.Run(verb+"/"+duration, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/dev/files/"+verb {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
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
						}
						total += n
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"files": 1, "bytes": total, "dest": "/workspace", "imported": "file"})
				}))
				defer server.Close()
				args := []string{"cella", verb, "dev"}
				if verb == "import" {
					args = append(args, "--input")
				}
				args = append(args, source, "--timeout", duration, "--api-url", server.URL)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				out, err := command.CombinedOutput()
				if strings.HasPrefix(duration, "-") {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "--timeout must not be negative") || requests.Load() != 0 {
						t.Errorf("negative timeout = %v, requests=%d: %s", err, requests.Load(), out)
					}
				} else if err != nil || requests.Load() != 1 {
					t.Errorf("valid timeout = %v, requests=%d: %s", err, requests.Load(), out)
				}
			})
		}
	}
}
