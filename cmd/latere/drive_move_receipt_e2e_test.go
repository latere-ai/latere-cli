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

func TestDriveMoveReceiptE2E(t *testing.T) {
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
		{"valid", `{"path":"files/to","moved_from":"files/from"}`, true},
		{"null", "null", false},
		{"empty", "{}", false},
		{"missing destination", `{"moved_from":"files/from"}`, false},
		{"missing source", `{"path":"files/to"}`, false},
		{"wrong destination", `{"path":"files/other","moved_from":"files/from"}`, false},
		{"wrong source", `{"path":"files/to","moved_from":"files/other"}`, false},
	} {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					var body struct {
						Destination string `json:"move_to"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Destination != "files/to" {
						t.Errorf("request body=%+v error=%v", body, err)
					}
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/files/me/files/from" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				args := []string{"drive", "mv", "files/from", "files/to", "--drive-url", server.URL, "--token", "synthetic-token"}
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
						if out.Len() != 0 || diagnostic.String() != "Moved files/from to files/to\n" {
							t.Errorf("output=%q stderr=%q", out.String(), diagnostic.String())
						}
					} else {
						var result struct {
							Path   string
							Source string `json:"moved_from"`
						}
						if json.Unmarshal(out.Bytes(), &result) != nil || result.Path != "files/to" || result.Source != "files/from" || diagnostic.Len() != 0 {
							t.Errorf("JSON=%q stderr=%q", out.String(), diagnostic.String())
						}
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "move receipt") || !strings.Contains(diagnostic.String(), "outcome is unknown") || strings.Contains(diagnostic.String(), "Moved") || out.Len() != 0 {
					t.Errorf("invalid receipt: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
