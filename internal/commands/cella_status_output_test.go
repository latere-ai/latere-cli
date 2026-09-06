// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCellaCommandStatusOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, dir, "synthetic-token"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "absent-auth.json"))
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, tc := range []struct {
		name, mode, response, status, stdout string
		exitCode                             int
	}{
		{"successful wait", "wait", `{"phase":"exited","exit_code":0}`, "phase=exited exit_code=0\n", "", 0},
		{"failed wait", "wait", `{"phase":"exited","exit_code":7}`, "phase=exited exit_code=7\n", "", 7},
		{"missing exit code", "wait", `{"phase":"failed"}`, "phase=failed\n", "", 1},
		{"logs", "logs", `{"phase":"exited","next_cursor":42,"bytes":"command logs"}`, "[cursor=42 phase=exited]\n", "command logs", 0},
	} {
		for _, limit := range []int{-1, 0, 3} {
			t.Run(fmt.Sprintf("%s/limit=%d", tc.name, limit), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					path := "/v1/sandboxes/dev/commands/cmd-1"
					if tc.mode == "logs" {
						path += "/logs"
					}
					if r.Method != http.MethodGet || r.URL.Path != path {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					if r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Error("incorrect authorization")
					}
					_, _ = io.WriteString(w, tc.response)
				}))
				defer server.Close()
				status := &evalOutputWriter{remaining: limit}
				if limit >= 0 {
					status.err = io.ErrClosedPipe
				}
				var out bytes.Buffer
				root := NewRoot("test")
				root.SetOut(&out)
				root.SetErr(status)
				root.SetArgs([]string{"cella", tc.mode, "dev", "cmd-1", "--api-url", server.URL})
				err := root.Execute()
				wantStatus, wantExit := tc.status, tc.exitCode
				if limit >= 0 {
					wantStatus = wantStatus[:limit]
					if wantExit == 0 {
						wantExit = 1
					}
					if !errors.Is(err, io.ErrClosedPipe) {
						t.Errorf("missing write error: %v", err)
					}
				}
				exitCode := 0
				if err != nil {
					exitCode = HandleExitError(io.Discard, err)
				}
				if exitCode != wantExit {
					t.Errorf("exit=%d want=%d error=%v", exitCode, wantExit, err)
				}
				if status.String() != wantStatus || out.String() != tc.stdout {
					t.Errorf("status=%q stdout=%q; want status=%q stdout=%q", status.String(), out.String(), wantStatus, tc.stdout)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d want=1", requests.Load())
				}
			})
		}
	}
}
