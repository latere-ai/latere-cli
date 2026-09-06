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
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedJSONConfiguredOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	token := filepath.Join(dir, "token.json")
	if err := os.WriteFile(token, []byte(`{"access_token":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                   string
		args                   []string
		path, body, key, value string
		requests               int32
	}{
		{"cella", []string{"cella", "list", "--json"}, "/v1/sandboxes", `[{"id":"sb-1"}]`, "id", "sb-1", 1},
		{"policy", []string{"cella", "policy", "--json"}, "/v1/policies", `[{"name":"restricted"}]`, "name", "restricted", 1},
		{"policy list", []string{"cella", "policy", "list", "--json"}, "/v1/policies", `[{"name":"restricted"}]`, "name", "restricted", 1},
		{"topos", []string{"topos", "agents", "list", "--json"}, "/v1/agents", `{"agents":[{"id":"agent-1"}]}`, "id", "agent-1", 1},
		{"lux", []string{"lux", "models", "--json"}, "/lux/v1/models", `{"items":[{"provider":"test","model":"model-1"}]}`, "model", "model-1", 2},
	} {
		for _, writable := range []string{"1", "0"} {
			t.Run(tc.name+"/writable="+writable, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					switch r.URL.Path {
					case tc.path:
						_, _ = io.WriteString(w, tc.body)
					case "/lux/v1/rates":
						_, _ = io.WriteString(w, `{"items":[]}`)
					default:
						t.Errorf("unexpected path=%s", r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				output := filepath.Join(t.TempDir(), "output.json")
				if err := os.WriteFile(output, nil, 0600); err != nil {
					t.Fatal(err)
				}
				// Reuse the helper that installs an inherited writer on the full command tree.
				args := append([]string{"-test.run=^TestCellaDownloadOutputHelperProcess$", "--"}, tc.args...)
				if tc.name == "lux" {
					args = append(args, "--lux-url", server.URL, "--token", "synthetic-token")
				} else {
					args = append(args, "--api-url", server.URL)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "TOPOS_TOKEN=synthetic-token", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_DOWNLOAD_OUTPUT="+output, "LATERE_TEST_DOWNLOAD_WRITABLE="+writable)
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				data, readErr := os.ReadFile(output)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if writable == "1" {
					var got []map[string]any
					if err != nil || diagnostic.Len() != 0 || json.Unmarshal(data, &got) != nil || len(got) != 1 || got[0][tc.key] != tc.value {
						t.Errorf("JSON=%q error=%v stderr=%q", data, err, diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || diagnostic.Len() == 0 || len(data) != 0 {
					t.Errorf("write failure: error=%v stderr=%q JSON=%q", err, diagnostic.String(), data)
				}
				if out.Len() != 0 {
					t.Errorf("JSON leaked to process stdout: %q", out.String())
				}
				if requests.Load() != tc.requests {
					t.Errorf("requests=%d, want %d", requests.Load(), tc.requests)
				}
			})
		}
	}
}
