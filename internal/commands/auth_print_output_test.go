// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestPrintTokenHonorsOutputWriter(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	if err := api.SaveAuthToken(api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "write failure"
		}
		t.Run(name, func(t *testing.T) {
			out := &failingEnvWriter{}
			var wantErr error
			if fail {
				wantErr = errors.New("output unavailable")
				out.failAt, out.err = 1, wantErr
			}
			cmd := newAuthPrintTokenCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetOut(out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(nil)
			if err := cmd.Execute(); !errors.Is(err, wantErr) {
				t.Errorf("output error = %v, want %v", err, wantErr)
			}
			if out.calls != 1 {
				t.Errorf("configured writer received %d writes, want 1", out.calls)
			}
			if !fail && out.String() != "synthetic-token\n" {
				t.Errorf("configured output = %q", out.String())
			}
		})
	}
}
