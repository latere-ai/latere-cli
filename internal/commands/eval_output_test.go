// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type evalOutputWriter struct {
	buffer    bytes.Buffer
	remaining int
	err       error
}

func (w *evalOutputWriter) Write(p []byte) (int, error) {
	if w.err == nil {
		return w.buffer.Write(p)
	}
	n := min(len(p), w.remaining)
	_, _ = w.buffer.Write(p[:n])
	w.remaining -= n
	return n, w.err
}

func (w *evalOutputWriter) String() string { return w.buffer.String() }

func TestEvalOutputFailures(t *testing.T) {
	evalTestEnv(t)
	manifest := filepath.Join(t.TempDir(), "suite.yaml")
	if err := os.WriteFile(manifest, []byte("suite: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		args     []string
		response string
		want     string
	}{
		{"apply", []string{"apply", "-f", manifest}, evalApplyResponse, "suite frontier-suite (created)\n  task abcdef012345 created\n  task fedcba987654 exists (lineage ln-7)\ncells: 4 created, 2 exists, 1 unmanaged\n  comparison model-vs-model: created, single-variable, 4 members\n  comparison sloppy: updated, confounded(harness, effort_configured), 6 members\nwarning: matrix.exclude[0] matched no cells\n"},
		{"dry-run", []string{"apply", "-f", manifest, "--dry-run"}, `{"dry_run":true,"suite":{"id":"st-1","name":"test","status":"created"}}`, "dry run — no changes written\nsuite test (created)\ncells: 0 created, 0 exists, 0 unmanaged\n"},
		{"empty suites", []string{"suites"}, `[]`, "No suites.\n"},
		{"empty cells", []string{"cells", "--suite", "st-1"}, `[]`, "No cells in this suite.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			for _, limit := range []int{-1, 0, 5} {
				sentinel := errors.New("output unavailable")
				out := &evalOutputWriter{remaining: max(0, limit)}
				if limit >= 0 {
					out.err = sentinel
				}
				cmd := newEvalCmd()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(out)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(append(append([]string{}, tc.args...), "--api-url", server.URL))
				err := cmd.Execute()
				if limit < 0 {
					if err != nil || out.String() != tc.want {
						t.Errorf("successful output: err=%v, got=%q want=%q", err, out.String(), tc.want)
					}
				} else {
					if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "write Eval") {
						t.Errorf("limit=%d: error=%v, want wrapped output error", limit, err)
					}
					if out.String() != tc.want[:limit] {
						t.Errorf("limit=%d: partial output=%q", limit, out.String())
					}
				}
			}
			if requests.Load() != 3 {
				t.Errorf("requests=%d, want 3 (no retry on output failure)", requests.Load())
			}
		})
	}
}
