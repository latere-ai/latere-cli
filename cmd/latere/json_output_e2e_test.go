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
	for _, tc := range []struct {
		name                   string
		args                   []string
		base                   string // the path the command's --api-url carries
		path, body, key, value string
		requests               int32
	}{
		{"cella", []string{"cella", "list", "--json"}, "/v1/environments", "/v1/environments/sandboxes", `{"items":[{"kind":"Sandbox","metadata":{"name":"dev"}}]}`, "kind", "Sandbox", 1},
		{"topos", []string{"topos", "agents", "list", "--json"}, "", "/v1/agents", `{"agents":[{"id":"agent-1"}]}`, "id", "agent-1", 1},
		{"models", []string{"models", "list", "--json"}, "", "/openai/v1/models", `{"object":"list","data":[{"id":"model-1","object":"model"}]}`, "id", "model-1", 1},
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
				args := append([]string{"-test.run=^TestConfiguredOutputHelperProcess$", "--"}, tc.args...)
				if tc.name == "models" {
					args = append(args, "--models-url", server.URL)
				} else {
					args = append(args, "--api-url", server.URL+tc.base)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_CELLA_TOKEN=synthetic-token", "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"), "XDG_CONFIG_HOME="+dir, "TOPOS_TOKEN=synthetic-token", "LATERE_MODEL_KEY=synthetic-token", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_DOWNLOAD_OUTPUT="+output, "LATERE_TEST_DOWNLOAD_WRITABLE="+writable)
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
