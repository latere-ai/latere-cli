// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"os/exec"
	"testing"
)

func TestQuoteShellValueRoundTrip(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	t.Setenv("BASH_ENV", "")
	t.Setenv("ENV", "")
	t.Setenv("SH_QUOTE_TEST", "expanded")
	for _, value := range []string{"", "abc.DEF-123_==", "https://host/path", " spaced ", "'", "\"", "one\ntwo", "a\tb", "${SH_QUOTE_TEST}", "`printf changed`", "a; printf changed", "*?[a]", "~", "雪"} {
		t.Run(value, func(t *testing.T) {
			quoted, err := quoteShellValue(value)
			if err != nil {
				t.Fatal(err)
			}
			got, err := exec.CommandContext(t.Context(), shell, "-c", "printf '%s' "+quoted).Output()
			if err != nil || string(got) != value {
				t.Errorf("shell round trip = %q (%v), want %q", got, err, value)
			}
		})
	}
}

func TestQuoteShellValueRejectsNUL(t *testing.T) {
	if value, err := quoteShellValue("a\x00b"); err == nil || value != "" {
		t.Errorf("NUL value encoded as %q (%v)", value, err)
	}
}
