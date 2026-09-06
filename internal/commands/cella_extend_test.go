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

func TestCellaExtendRejectsInvalidDeadlineBeforeAuthentication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "absent-token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	for _, tc := range []struct{ value, message string }{
		{"", "--deadline must be RFC3339"},
		{"not-a-date", "--deadline must be RFC3339"},
		{"0001-01-01T00:00:00Z", "--deadline must be in the future"},
		{"2000-01-01T00:00:00Z", "--deadline must be in the future"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			cmd := newCeExtendCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"dev", "--deadline", tc.value})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Errorf("invalid deadline returned %v, want %q", err, tc.message)
			}
		})
	}
}
