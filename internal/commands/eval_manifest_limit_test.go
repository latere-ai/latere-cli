// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvalResolvedManifestLimit(t *testing.T) {
	const overhead = len("suite: test\ntasks:\n    - prompt: file://prompt.md\n      prompt_text: \n")
	for _, tc := range []struct {
		name, prompt string
		refs         int
		valid        bool
	}{
		{"valid large prompt", strings.Repeat("x", 100<<10), 1, true},
		{"exact resolved limit", strings.Repeat("x", (256<<10)-overhead), 1, true},
		{"over resolved limit", strings.Repeat("x", (256<<10)-overhead+1), 1, false},
		{"oversized prompt", strings.Repeat("x", (256<<10)+1), 1, false},
		{"combined prompts", strings.Repeat("x", 140<<10), 2, false},
		{"YAML expansion", strings.Repeat("x\n", 50<<10), 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(tc.prompt), 0600); err != nil {
				t.Fatal(err)
			}
			body := "suite: test\ntasks:\n" + strings.Repeat("  - prompt: file://prompt.md\n", tc.refs)
			out, err := resolvePromptRefs([]byte(body), dir)
			if tc.valid {
				if err != nil || len(out) != len(tc.prompt)+overhead || len(out) > 256<<10 {
					t.Errorf("valid manifest: bytes=%d error=%v", len(out), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "limit") || len(out) != 0 {
				t.Errorf("oversized manifest: bytes=%d error=%v", len(out), err)
			}
		})
	}
}
