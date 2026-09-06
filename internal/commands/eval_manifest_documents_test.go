// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvalManifestDocuments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("prompt\n---\ntext\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, refs := range []bool{false, true} {
		first := "suite: first\n"
		if refs {
			first += "tasks:\n  - prompt: file://prompt.md\n"
		}
		for _, tc := range []struct {
			name, body string
			valid      bool
		}{
			{"single", first, true},
			{"explicit start", "---\n" + first, true},
			{"explicit end", first + "...\n# comment\n", true},
			{"trailing comments", first + "\n# ---\n", true},
			{"second suite", first + "---\nsuite: second\n", false},
			{"empty second", first + "---\n", false},
			{"invalid second", first + "---\nsuite: [\n", false},
			{"trailing garbage", first + "...\ngarbage\n", false},
		} {
			name := tc.name
			if refs {
				name += " with prompt refs"
			}
			t.Run(name, func(t *testing.T) {
				out, err := resolvePromptRefs([]byte(tc.body), dir)
				if tc.valid {
					if err != nil {
						t.Fatal(err)
					}
					if !refs && string(out) != tc.body {
						t.Errorf("valid manifest changed: %q", out)
					}
					if refs && !strings.Contains(string(out), "prompt_text:") {
						t.Errorf("prompt not resolved: %q", out)
					}
				} else if err == nil || !strings.Contains(err.Error(), "manifest") || len(out) != 0 {
					t.Errorf("invalid stream: error=%v output=%q", err, out)
				}
			})
		}
	}
	t.Run("validate before reading references", func(t *testing.T) {
		_, err := resolvePromptRefs([]byte("tasks:\n  - prompt: file://missing.md\n---\nsuite: second\n"), dir)
		if err == nil || !strings.Contains(err.Error(), "one YAML document") {
			t.Errorf("error=%v, want document validation before file access", err)
		}
	})
}
