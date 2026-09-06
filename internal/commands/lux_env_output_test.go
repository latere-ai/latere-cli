// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"testing"
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

func TestLuxEnvPropagatesOutputErrors(t *testing.T) {
	for _, tc := range []struct {
		name               string
		raw, alias, stderr bool
		failAt             int
	}{
		{name: "first export", failAt: 1},
		{name: "second export", failAt: 2},
		{name: "export provenance", stderr: true, failAt: 1},
		{name: "raw token", raw: true, failAt: 1},
		{name: "raw provenance", raw: true, stderr: true, failAt: 1},
		{name: "legacy token", alias: true, failAt: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, auth, token := "https://lux.example", "", "synthetic-token"
			cmd := newLuxEnvCmd(&url, &auth, &token)
			args := []string{"--compat", "openai"}
			if tc.raw {
				args = []string{"--raw"}
			}
			if tc.alias {
				cmd = newLuxTokenCmd(&url, &auth, &token)
				args = nil
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
