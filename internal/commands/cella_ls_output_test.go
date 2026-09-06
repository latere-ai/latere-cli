// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestCellaLsOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes/dev/files" || r.URL.Query().Get("path") != "/workspace" || r.URL.Query().Get("list") != "true" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"entries":[{"name":"a.txt","size":3,"mode":420},{"name":"dir","size":0,"mode":493,"is_directory":true},{"name":"last","size":7,"mode":384}]}`)
	}))
	defer server.Close()
	for _, failAt := range []int{0, 1, 2} {
		out := &failingEnvWriter{}
		var wantErr error
		if failAt > 0 {
			wantErr = errors.New("output unavailable")
			out.failAt, out.err = failAt, wantErr
		}
		cmd := newCeLsCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetOut(out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"dev", "/workspace", "--api-url", server.URL})
		err := cmd.Execute()
		if !errors.Is(err, wantErr) {
			t.Errorf("failAt=%d: error=%v, want %v", failAt, err, wantErr)
		}
		if failAt == 0 {
			if out.String() != "0644\t3\ta.txt\n0755\t0\tdir/\n0600\t7\tlast\n" {
				t.Errorf("directory listing=%q", out.String())
			}
		} else if out.calls != failAt || err == nil || !strings.Contains(err.Error(), "write directory listing") {
			t.Errorf("failAt=%d: writes=%d error=%v", failAt, out.calls, err)
		}
	}
}
