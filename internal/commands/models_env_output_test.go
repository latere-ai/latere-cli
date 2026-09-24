// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"testing"

	"github.com/latere-ai/latere-cli/internal/modelkey"
)

type failingEnvWriter struct {
	bytes.Buffer
	calls, failAt int
	err           error
}

func (w *failingEnvWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, w.err
	}
	return w.Buffer.Write(p)
}

// TestModelsEnvPropagatesOutputErrors: a write that fails stops the command
// with that error, and nothing is reported after it.
func TestModelsEnvPropagatesOutputErrors(t *testing.T) {
	t.Setenv(modelkey.EnvKey, "pat_ci.value")
	for _, tc := range []struct {
		name        string
		raw, stderr bool
		failAt      int
	}{
		{name: "first export", failAt: 1},
		{name: "second export", failAt: 2},
		{name: "export provenance", stderr: true, failAt: 1},
		{name: "raw key", raw: true, failAt: 1},
		{name: "raw provenance", raw: true, stderr: true, failAt: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, auth := "https://models.example/v1/models", ""
			cmd := newModelsEnvCmd(&url, &auth)
			var args []string
			if tc.raw {
				args = []string{"--raw"}
			}
			writeErr := errors.New("output unavailable")
			out, diagnostic := &failingEnvWriter{}, &failingEnvWriter{}
			failed := out
			if tc.stderr {
				failed = diagnostic
			}
			failed.failAt, failed.err = tc.failAt, writeErr
			cmd.SetOut(out)
			cmd.SetErr(diagnostic)
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			if err := cmd.Execute(); !errors.Is(err, writeErr) {
				t.Errorf("write error was lost: %v", err)
			}
			if failed.calls != tc.failAt {
				t.Errorf("continued writing after failure: calls=%d, want %d", failed.calls, tc.failAt)
			}
			if !tc.stderr && diagnostic.Len() != 0 {
				t.Errorf("reported provenance after failed output: %q", diagnostic.String())
			}
		})
	}
}
