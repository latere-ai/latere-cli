// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCellaWriteRejectsOversizedInputBeforeAuthentication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "absent-token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	input := filepath.Join(root, "large.bin")
	file, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((10 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := newCeWriteCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"dev", "/workspace/file", "--input", input})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "10 MiB write limit") || !strings.Contains(err.Error(), "upload") {
		t.Errorf("oversized input returned %v", err)
	}
}
