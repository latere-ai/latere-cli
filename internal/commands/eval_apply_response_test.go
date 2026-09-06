// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEvalApplyResponseValidation(t *testing.T) {
	evalTestEnv(t)
	manifest := filepath.Join(t.TempDir(), "suite.yaml")
	if err := os.WriteFile(manifest, []byte("suite: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{false, true} {
		valid := fmt.Sprintf(`{"dry_run":%t,"suite":{"id":"st-1","name":"test","status":"created"}}`, dry)
		for _, tc := range []struct {
			name, body string
			valid      bool
		}{
			{"valid", valid, true},
			{"future status", strings.Replace(valid, `"created"`, `"reconciled","extra":true`, 1), true},
			{"null", `null`, false},
			{"empty", `{}`, false},
			{"missing suite", fmt.Sprintf(`{"dry_run":%t}`, dry), false},
			{"missing id", strings.Replace(valid, `"id":"st-1",`, "", 1), false},
			{"missing name", strings.Replace(valid, `"name":"test",`, "", 1), false},
			{"missing status", strings.Replace(valid, `,"status":"created"`, "", 1), false},
			{"blank status", strings.Replace(valid, `"created"`, `" \n"`, 1), false},
			{"missing mode", strings.Replace(valid, fmt.Sprintf(`"dry_run":%t,`, dry), "", 1), false},
			{"null mode", strings.Replace(valid, fmt.Sprintf(`"dry_run":%t`, dry), `"dry_run":null`, 1), false},
			{"wrong mode", strings.Replace(valid, fmt.Sprintf(`"dry_run":%t`, dry), fmt.Sprintf(`"dry_run":%t`, !dry), 1), false},
		} {
			t.Run(fmt.Sprintf("dry=%t/%s", dry, tc.name), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/suites/apply" || (r.URL.Query().Get("dry_run") == "1") != dry {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				args := []string{"apply", "-f", manifest, "--api-url", server.URL}
				if dry {
					args = append(args, "--dry-run")
				}
				cmd := newEvalCmd()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				err := cmd.Execute()
				out := output.String()
				if tc.valid {
					if err != nil || !strings.Contains(out, "suite test") || strings.Contains(out, "dry run") != dry {
						t.Errorf("valid response: error=%v output=%q", err, out)
					}
				} else if err == nil || !strings.Contains(err.Error(), "invalid Eval apply response") || !strings.Contains(err.Error(), "outcome is unknown") || out != "" {
					t.Errorf("invalid response: error=%v output=%q", err, out)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
