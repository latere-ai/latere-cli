// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestCellaWaitRejectsInvalidTimeoutBeforeAuthentication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "absent-token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	for _, value := range []string{"0", "-1", "9223372037", "18446744074", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			cmd := newCeWaitCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"dev", "command", "--timeout", value})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--timeout") {
				t.Errorf("invalid timeout returned %v", err)
			}
		})
	}
}
