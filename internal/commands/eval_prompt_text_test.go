// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEvalResolvedPromptText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing.md"), []byte("changed local prompt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ref, field, want string
		resolved               bool
	}{
		{"existing file", "existing.md", "prompt_text: pinned prompt", "pinned prompt", true},
		{"missing file", "missing.md", "prompt_text: pinned prompt", "pinned prompt", true},
		{"directory provenance", "directory", "prompt_text: pinned prompt", "pinned prompt", true},
		{"whitespace text", "existing.md", `prompt_text: " \n"`, " \n", true},
		{"empty text", "existing.md", `prompt_text: ""`, "changed local prompt\n", false},
		{"absent text", "existing.md", "", "changed local prompt\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf("# retain resolved manifests verbatim\nsuite: test\ntasks:\n  - prompt: file://%s\n    %s\n", tc.ref, tc.field))
			out, err := resolvePromptRefs(body, dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.resolved && string(out) != string(body) {
				t.Errorf("resolved manifest changed: %s", out)
			}
			var got struct {
				Tasks []struct {
					Prompt string `yaml:"prompt"`
					Text   string `yaml:"prompt_text"`
				} `yaml:"tasks"`
			}
			if err := yaml.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Tasks) != 1 || got.Tasks[0].Prompt != "file://"+tc.ref || got.Tasks[0].Text != tc.want {
				t.Errorf("tasks=%+v, want ref=%q text=%q", got.Tasks, tc.ref, tc.want)
			}
		})
	}
}
