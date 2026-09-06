// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
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

func TestOneShotRunStatusResponseE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	token := filepath.Join(root, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"status", "cancel"} {
		for _, asJSON := range []bool{false, true} {
			for _, tc := range []struct {
				name, body string
				valid      bool
			}{
				{"missing", `{}`, false}, {"null", `null`, false}, {"no content", "", false},
				{"missing ID", `{"state":"cancelled"}`, false},
				{"wrong ID", `{"run_id":"other","state":"cancelled"}`, false},
				{"missing state", `{"run_id":"run-123"}`, false},
				{"blank state", `{"run_id":"run-123","state":" \t"}`, false},
				{"valid", `{"run_id":"run-123","state":"cancelled"}`, true},
			} {
				t.Run(fmt.Sprintf("%s/json=%v/%s", verb, asJSON, tc.name), func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						method := http.MethodGet
						if verb == "cancel" {
							method = http.MethodDelete
						}
						if r.Method != method || r.URL.Path != "/v1/one-shot-runs/run-123" {
							t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						}
						if tc.body == "" {
							w.WriteHeader(204)
							return
						}
						_, _ = w.Write([]byte(tc.body))
					}))
					defer server.Close()
					args := []string{"cella", "run", verb, "run-123", "--api-url", server.URL}
					if asJSON {
						args = append(args, "--json")
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					if tc.valid {
						printed := diagnostic.String()
						if asJSON {
							printed = out.String()
						}
						if err != nil || !strings.Contains(printed, "run-123") || !strings.Contains(printed, "cancelled") {
							t.Errorf("valid response failed: %v, output=%q %q", err, out.String(), diagnostic.String())
						}
					} else {
						if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "run response") {
							t.Errorf("invalid response returned %v: %s", err, diagnostic.String())
						}
						if out.Len() != 0 || strings.Contains(diagnostic.String(), "run_id=") {
							t.Errorf("invalid response printed as result: %q %q", out.String(), diagnostic.String())
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
