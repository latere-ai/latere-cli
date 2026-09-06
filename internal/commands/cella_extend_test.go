// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestCellaExtendRejectsInvalidHoursBeforeAuthentication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "absent-token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	for _, hours := range []string{"0", "-1", "-24"} {
		t.Run(hours, func(t *testing.T) {
			cmd := newCeExtendCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"dev", "--hours", hours})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--hours must be greater than zero") {
				t.Errorf("invalid hours returned %v", err)
			}
		})
	}
}
