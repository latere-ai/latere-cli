// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSharedJSONConfiguredOutput(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "synthetic-token"))
	t.Setenv("TOPOS_TOKEN", "synthetic-token")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, tc := range []struct {
		name                   string
		args                   []string
		path, body, key, value string
		requests               int32
	}{
		{"cella", []string{"cella", "list", "--json"}, "/v1/sandboxes", `[{"id":"sb-1","name":"dev"}]`, "id", "sb-1", 1},
		{"policy", []string{"cella", "policy", "--json"}, "/v1/policies", `[{"name":"restricted"}]`, "name", "restricted", 1},
		{"policy list", []string{"cella", "policy", "list", "--json"}, "/v1/policies", `[{"name":"restricted"}]`, "name", "restricted", 1},
		{"topos", []string{"topos", "agents", "list", "--json"}, "/v1/agents", `{"agents":[{"id":"agent-1"}]}`, "id", "agent-1", 1},
		{"lux", []string{"lux", "models", "--json"}, "/lux/v1/models", `{"items":[{"provider":"test","model":"model-1"}]}`, "model", "model-1", 2},
	} {
		for _, failAfter := range []int{-1, 0, 3} {
			t.Run(fmt.Sprintf("%s/failAfter=%d", tc.name, failAfter), func(t *testing.T) {
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
				out := &evalOutputWriter{}
				sentinel := errors.New("JSON output unavailable")
				if failAfter >= 0 {
					out.remaining, out.err = failAfter, sentinel
				}
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				args := append([]string(nil), tc.args...)
				if tc.name == "lux" {
					args = append(args, "--lux-url", server.URL, "--token", "synthetic-token")
				} else {
					args = append(args, "--api-url", server.URL)
				}
				root.SetArgs(args)
				leaked, err := captureStdout(root.Execute)
				if failAfter < 0 {
					if err != nil {
						t.Errorf("JSON output: %v", err)
					}
					var got []map[string]any
					if json.Unmarshal([]byte(out.String()), &got) != nil || len(got) != 1 || got[0][tc.key] != tc.value {
						t.Errorf("configured JSON=%q", out.String())
					}
					if !strings.HasPrefix(out.String(), "[\n  {") || !strings.HasSuffix(out.String(), "\n") {
						t.Errorf("JSON formatting changed: %q", out.String())
					}
				} else if !errors.Is(err, sentinel) || len(out.String()) != failAfter {
					t.Errorf("error=%v output=%q, want failure after %d bytes", err, out.String(), failAfter)
				}
				if leaked != "" {
					t.Errorf("JSON leaked to process stdout: %q", leaked)
				}
				if requests.Load() != tc.requests {
					t.Errorf("requests=%d, want %d", requests.Load(), tc.requests)
				}
			})
		}
	}
}
