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

func TestDriveShareReceiptE2E(t *testing.T) {
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
			{"pending", "status", "pending", true},
			{"existing", "existing", true, true},
			{"null", "", nil, false},
			{"missing id", "id", nil, false},
			{"revoked", "status", "revoked", false},
			{"wrong permission", "permission", "manage", false},
			{"wrong prefix", "path_prefix", "files/other", false},
			{"wrong grantee", "grantee_type", "public", false},
			{"wrong owner", "owner", "o-other", false},
			{"missing url", "url", nil, false},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				body := map[string]any{"id": "share-1", "status": "active", "permission": "read", "grantee_type": "link", "path_prefix": "files/item", "owner": "o-example", "url": "/s/synthetic-link"}
				body[tc.field] = tc.value
				if tc.name == "pending" {
					delete(body, "url")
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					var fields map[string]string
					if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
						t.Error(err)
						return
					}
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/shares" || r.Header.Get("Authorization") != "Bearer synthetic-token" || fields["owner"] != "o-example" || fields["path_prefix"] != "files/item" || fields["grantee_type"] != "link" || fields["permission"] != "read" {
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
				args := []string{"drive", "share", "files/item", "--link", "--owner", "o-example", "--drive-url", server.URL, "--token", "synthetic-token"}
				if format == "json" {
					args = append(args, "--json")
				}
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
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
						want := ""
						if tc.name != "pending" {
							want = server.URL + "/s/synthetic-link\n"
						}
						if out.String() != want || !strings.Contains(diagnostic.String(), "id share-1") {
							t.Errorf("stdout=%q stderr=%q", out.String(), diagnostic.String())
						}
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "share creation receipt") || !strings.Contains(diagnostic.String(), "outcome is unknown") || strings.Contains(diagnostic.String(), "Share created") || strings.Contains(diagnostic.String(), "synthetic-link") {
					t.Errorf("invalid receipt: error=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
