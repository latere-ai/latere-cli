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

func TestDriveRestoreReceiptE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `{"path":"files/item","restored_version":3,"size":7,"checksum":"opaque"}`, true},
		{"null", "null", false},
		{"empty", "{}", false},
		{"missing path", `{"restored_version":3}`, false},
		{"wrong path", `{"path":"files/other","restored_version":3}`, false},
		{"missing version", `{"path":"files/item"}`, false},
		{"wrong version", `{"path":"files/item","restored_version":2}`, false},
	} {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					var body struct {
						Version int `json:"restore_version"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version != 3 {
						t.Errorf("request body=%+v error=%v", body, err)
					}
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/files/me/files/item" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				args := []string{"drive", "restore", "files/item", "--version", "3", "--drive-url", server.URL, "--token", "synthetic-token"}
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
					if err != nil {
						t.Errorf("valid receipt: %v: %s", err, diagnostic.String())
					}
					if format == "text" {
						if out.Len() != 0 || diagnostic.String() != "Restored files/item to version 3\n" {
							t.Errorf("output=%q stderr=%q", out.String(), diagnostic.String())
						}
					} else {
						var result struct {
							Path     string
							Version  int `json:"restored_version"`
							Size     int
							Checksum string
						}
						if json.Unmarshal(out.Bytes(), &result) != nil || result.Path != "files/item" || result.Version != 3 || result.Size != 7 || result.Checksum != "opaque" || diagnostic.Len() != 0 {
							t.Errorf("JSON=%q stderr=%q", out.String(), diagnostic.String())
						}
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "restore receipt") || !strings.Contains(diagnostic.String(), "outcome is unknown") || strings.Contains(diagnostic.String(), "Restored") || out.Len() != 0 {
					t.Errorf("invalid receipt: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
