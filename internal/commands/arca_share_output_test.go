// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The address of a token grant goes to stdout, so it can be piped, and a
// subject grant writes nothing there. The token is answered once, so a
// stdout that cannot be written to is an error naming the share rather
// than a silent loss.
func TestArcaShareURLOutput(t *testing.T) {
	for _, token := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				wantPath := "/v1/shares"
				if token {
					wantPath = "/v1/shares/links"
				}
				if r.Method != http.MethodPost || r.URL.Path != wantPath {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				body := map[string]any{
					"id": "share-1", "status": "active", "permission": "read",
					"grantee_kind": "subject", "grantee": "https://auth.latere.ai|7c22",
					"path_prefix": "files/item", "owner": "https://auth.latere.ai|9ab3",
				}
				if token {
					body["grantee_kind"] = "link"
					delete(body, "grantee")
					body["token"] = "synthetic-link-value"
					body["url"] = "/v1/shares/links/synthetic-link-value"
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(body)
			}))
			out := &failingEnvWriter{}
			sentinel := errors.New("stdout unavailable")
			if fail {
				out.failAt, out.err = 1, sentinel
			}
			var diagnostic bytes.Buffer
			cmd := newArcaCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetOut(out)
			cmd.SetErr(&diagnostic)
			args := []string{"--api-url", server.URL, "--token", "synthetic-token", "share", "files/item"}
			if token {
				args = append(args, "--link")
			} else {
				args = append(args, "--to", "https://auth.latere.ai|7c22")
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			if fail && token {
				if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "write the address") || !strings.Contains(err.Error(), "share-1") {
					t.Errorf("token=%t: error=%v, want the write failure naming the share", token, err)
				}
			} else if err != nil {
				t.Errorf("token=%t fail=%t: %v", token, fail, err)
			}
			want := ""
			if token && !fail {
				want = server.URL + "/v1/shares/links/synthetic-link-value\n"
			}
			if out.String() != want {
				t.Errorf("output=%q, want %q", out.String(), want)
			}
			if !token && out.calls != 0 {
				t.Errorf("a subject grant wrote to stdout %d times", out.calls)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
			server.Close()
		}
	}
}
