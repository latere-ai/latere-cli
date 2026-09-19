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

func TestArcaShareReceiptE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, format := range []string{"text", "json"} {
		for _, tc := range []struct {
			name, field string
			value       any
			valid       bool
		}{
			{"active", "extra", true, true},
			{"null", "", nil, false},
			{"missing id", "id", nil, false},
			{"revoked", "status", "revoked", false},
			{"above read", "permission", "write", false},
			{"wrong prefix", "path_prefix", "files/other", false},
			{"wrong kind", "grantee_kind", "public", false},
			{"wrong owner", "owner", "https://auth.latere.ai|7c22", false},
			{"missing token", "token", nil, false},
			{"missing url", "url", nil, false},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				body := map[string]any{
					"id": "share-1", "status": "active", "permission": "read", "grantee_kind": "link",
					"path_prefix": "files/item", "owner": "https://auth.latere.ai|9ab3",
					"token": "synthetic-link-value", "url": "/v1/shares/links/synthetic-link-value",
				}
				if tc.value == nil && tc.field != "" {
					delete(body, tc.field)
				} else {
					body[tc.field] = tc.value
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					var fields map[string]string
					if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
						t.Error(err)
						return
					}
					if r.Method != http.MethodPost || r.URL.Path != "/v1/shares/links" || r.Header.Get("Authorization") != "Bearer synthetic-token" || fields["owner"] != "https://auth.latere.ai|9ab3" || fields["path_prefix"] != "files/item" || fields["kind"] != "link" {
						t.Errorf("request=%s %s body=%+v", r.Method, r.URL, fields)
					}
					w.WriteHeader(http.StatusCreated)
					if tc.name == "null" {
						_, _ = fmt.Fprint(w, "null")
					} else {
						_ = json.NewEncoder(w).Encode(body)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				args := []string{"arca", "share", "files/item", "--link", "--owner", "https://auth.latere.ai|9ab3", "--api-url", server.URL, "--token", "synthetic-token"}
				if format == "json" {
					args = append(args, "--json")
				}
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				if tc.valid {
					if err != nil {
						t.Fatalf("valid receipt: error=%v stderr=%q", err, diagnostic.String())
					}
					if format == "json" {
						var got map[string]any
						if json.Unmarshal(out.Bytes(), &got) != nil || got["id"] != "share-1" || got["status"] != body["status"] || diagnostic.Len() != 0 {
							t.Errorf("JSON=%q stderr=%q", out.String(), diagnostic.String())
						}
					} else {
						want := server.URL + "/v1/shares/links/synthetic-link-value\n"
						if out.String() != want || !strings.Contains(diagnostic.String(), "id share-1") {
							t.Errorf("stdout=%q stderr=%q", out.String(), diagnostic.String())
						}
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "share creation receipt") || !strings.Contains(diagnostic.String(), "outcome is unknown") || strings.Contains(diagnostic.String(), "Shared files/item") || strings.Contains(diagnostic.String(), "synthetic-link-value") {
					t.Errorf("invalid receipt: error=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
