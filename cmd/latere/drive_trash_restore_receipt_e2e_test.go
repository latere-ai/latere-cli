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

func TestDriveTrashRestoreReceiptE2E(t *testing.T) {
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
		{"valid", `{"path":"files/item","status":"restored","extra":true}`, true},
		{"null", `null`, false},
		{"empty", `{}`, false},
		{"missing path", `{"status":"restored"}`, false},
		{"wrong path", `{"path":"files/other","status":"restored"}`, false},
		{"missing status", `{"path":"files/item"}`, false},
		{"pending", `{"path":"files/item","status":"pending"}`, false},
		{"failed", `{"path":"files/item","status":"failed"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct{ Owner, Path string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Owner != "org:example" || body.Path != "files/item" {
					t.Errorf("request body=%+v error=%v", body, err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/trash/restore" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "drive", "restore", "files/item", "--owner", "org:example", "--drive-url", server.URL, "--token", "synthetic-token")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			err := command.Run()
			if tc.valid {
				if err != nil || out.Len() != 0 || diagnostic.String() != "Restored files/item from trash\n" {
					t.Errorf("valid receipt: error=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
				}
			} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "trash restore receipt") || !strings.Contains(diagnostic.String(), "outcome is unknown") || strings.Contains(diagnostic.String(), "Restored") || out.Len() != 0 {
				t.Errorf("invalid receipt: error=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}
