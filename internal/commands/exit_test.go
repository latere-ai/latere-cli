// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestHandleExitError pins the verdict-in-exit-code contract: a completed
// debate with unresolved attacks exits 2 and prints nothing (the summary is
// already on stdout), while any other error exits 1 and is printed.
func TestHandleExitError(t *testing.T) {
	t.Run("unresolved -> 2, silent", func(t *testing.T) {
		var buf bytes.Buffer
		code := HandleExitError(&buf, &unresolvedError{n: 3})
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if buf.Len() != 0 {
			t.Errorf("expected no output, got %q", buf.String())
		}
	})

	t.Run("wrapped unresolved -> 2", func(t *testing.T) {
		var buf bytes.Buffer
		err := fmt.Errorf("review: %w", &unresolvedError{n: 1})
		if code := HandleExitError(&buf, err); code != 2 {
			t.Errorf("exit code = %d, want 2 (errors.As must unwrap)", code)
		}
	})

	t.Run("generic error -> 1, printed", func(t *testing.T) {
		var buf bytes.Buffer
		code := HandleExitError(&buf, errors.New("boom"))
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if got := buf.String(); got != "boom\n" {
			t.Errorf("output = %q, want %q", got, "boom\n")
		}
	})
}

func TestRemoteExit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code, want int
		diagnostic string
	}{
		{"success", 0, 0, ""},
		{"command failure", 7, 7, ""},
		{"maximum code", 255, 255, ""},
		{"killed", 137, 137, ""},
		{"negative sentinel", -1, 1, "invalid exit code -1"},
		{"overflow", 256, 1, "invalid exit code 256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := remoteExit(tc.code)
			if tc.want == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed execution reported success")
			}
			var buf bytes.Buffer
			if got := HandleExitError(&buf, fmt.Errorf("execution: %w", err)); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
			if tc.diagnostic == "" {
				if buf.Len() != 0 {
					t.Errorf("unexpected diagnostic: %q", buf.String())
				}
			} else if !strings.Contains(buf.String(), tc.diagnostic) {
				t.Errorf("diagnostic = %q, want %q", buf.String(), tc.diagnostic)
			}
		})
	}
}
