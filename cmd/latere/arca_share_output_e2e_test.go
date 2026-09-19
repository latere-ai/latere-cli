// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestArcaShareOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, format := range []string{"text", "json"} {
		{
			for _, writable := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/writable=%t", format, writable), func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Method != http.MethodPost || r.URL.Path != "/v1/shares/links" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Errorf("unexpected request: %s %s", r.Method, r.URL)
						}
						w.WriteHeader(http.StatusCreated)
						_ = json.NewEncoder(w).Encode(map[string]any{
							"id": "share-1", "status": "active", "permission": "read", "grantee_kind": "link",
							"path_prefix": "files/item", "owner": "https://auth.latere.ai|9ab3",
							"token": "synthetic-link-value", "url": "/v1/shares/links/synthetic-link-value",
						})
					}))
					defer server.Close()
					output := filepath.Join(t.TempDir(), "output")
					const previous = "existing output\n"
					if err := os.WriteFile(output, []byte(previous), 0600); err != nil {
						t.Fatal(err)
					}
					flags := os.O_RDONLY
					if writable {
						flags = os.O_WRONLY | os.O_APPEND
					}
					file, err := os.OpenFile(output, flags, 0600)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					args := []string{"arca", "--api-url", server.URL, "--token", "synthetic-token", "share", "files/item", "--link"}
					if format == "json" {
						args = append(args, "--json")
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var diagnostic bytes.Buffer
					command.Stdout, command.Stderr = file, &diagnostic
					err = command.Run()
					data, readErr := os.ReadFile(output)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if writable {
						if err != nil {
							t.Errorf("successful output: %v: %s", err, diagnostic.String())
						}
						if !strings.HasPrefix(string(data), previous) {
							t.Fatalf("existing output changed: %q", data)
						}
						result := data[len(previous):]
						if format == "text" {
							if string(result) != server.URL+"/v1/shares/links/synthetic-link-value\n" {
								t.Errorf("URL=%q", result)
							}
						} else {
							var got struct {
								ID, URL, Token string
							}
							if json.Unmarshal(result, &got) != nil || got.ID != "share-1" ||
								got.URL != "/v1/shares/links/synthetic-link-value" || got.Token != "synthetic-link-value" {
								t.Errorf("JSON=%q", result)
							}
						}
					} else {
						if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || diagnostic.Len() == 0 {
							t.Errorf("failed output: err=%v stderr=%q", err, diagnostic.String())
						}
						if format == "text" && (!strings.Contains(diagnostic.String(), "write the address") || !strings.Contains(diagnostic.String(), "share-1")) {
							t.Errorf("missing share recovery diagnostic: %q", diagnostic.String())
						}
						if string(data) != previous {
							t.Errorf("failed write changed file: %q", data)
						}
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d, want 1", requests.Load())
					}
				})
			}
		}
	}
}
