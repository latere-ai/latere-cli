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
		{"integer text", "missing.md", "prompt_text: 001", "001", true},
		{"boolean text", "missing.md", "prompt_text: false", "false", true},
		{"float text", "existing.md", "prompt_text: 1.0", "1.0", true},
		{"exponent text", "existing.md", "prompt_text: 1e3", "1e3", true},
		{"date text", "missing.md", "prompt_text: 2026-09-06", "2026-09-06", true},
		{"null text", "existing.md", "prompt_text: null", "changed local prompt\n", false},
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

func TestEvalPromptResolutionPreservesScalarSpellings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fresh.md"), []byte("fresh prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"suite: 001\norg: false\ntasks:\n  - prompt: 1e3\n  - prompt: file://fresh.md\nmatrix:\n  model:\n    - id: 1.0\n  harness:\n    - version: 2026-09-06\n",
		"suite: 001\norg: false\ntasks:\n  - &inline {prompt: 1e3}\n  - *inline\n  - &file {prompt: file://fresh.md}\n  - *file\nmatrix:\n  model: [{id: 1.0}]\n  harness: [{version: 2026-09-06}]\n",
	} {
		var got struct {
			Suite, Org string
			Tasks      []struct {
				Prompt string `yaml:"prompt"`
				Text   string `yaml:"prompt_text"`
			}
			Matrix struct {
				Model   []struct{ ID string }
				Harness []struct{ Version string }
			}
		}
		out, err := resolvePromptRefs([]byte(body), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if got.Suite != "001" || got.Org != "false" || len(got.Matrix.Model) != 1 || got.Matrix.Model[0].ID != "1.0" || len(got.Matrix.Harness) != 1 || got.Matrix.Harness[0].Version != "2026-09-06" {
			t.Errorf("manifest scalar spellings changed: %s", out)
		}
		for i, task := range got.Tasks {
			if task.Prompt == "file://fresh.md" {
				if task.Text != "fresh prompt" {
					t.Errorf("task %d: %+v", i, task)
				}
			} else if task.Prompt != "1e3" {
				t.Errorf("task %d: prompt changed to %q", i, task.Prompt)
			}
		}
	}
}

func TestEvalPromptResolutionMerges(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{"first.md": "first", "second.md": "second"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{
		"suite: &key prompt_text\ntasks:\n  - {prompt: file://first.md, *key: null}\n  - {prompt: inline}\n  - {prompt: file://second.md}\n  - {prompt: file://first.md}\n",
		"tasks:\n  - {prompt: file://first.md, prompt_text: &empty ''}\n  - {prompt: inline, prompt_text: *empty}\n  - {prompt: file://second.md}\n  - {prompt: file://first.md}\n",
		"tasks:\n  - &first {prompt: file://first.md}\n  - {<<: *first, prompt: inline}\n  - {<<: *first, prompt: file://second.md}\n  - *first\n",
		"<<:\n  tasks: &tasks\n    - &first {prompt: file://first.md, prompt_text: null}\n    - {<<: *first, prompt: inline}\n    - {<<: [*first], prompt: file://second.md}\n    - *first\ntasks: *tasks\n",
	} {
		out, err := resolvePromptRefs([]byte(body), dir)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tasks []struct {
				Prompt string `yaml:"prompt"`
				Text   string `yaml:"prompt_text"`
			}
		}
		if err := yaml.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		want := []string{"first", "inline", "second", "first"}
		if len(got.Tasks) != len(want) {
			t.Fatalf("tasks=%+v", got.Tasks)
		}
		for i, task := range got.Tasks {
			text := task.Text
			if text == "" {
				text = task.Prompt
			}
			if text != want[i] {
				t.Errorf("task %d: got %q want %q; YAML:\n%s", i, text, want[i], out)
			}
		}
	}
}

func TestEvalPromptResolutionBinaryText(t *testing.T) {
	dir := t.TempDir()
	want := string([]byte{'a', 0xff, 'b'})
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := resolvePromptRefs([]byte("tasks: [{prompt: file://prompt.md}]"), dir)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tasks []struct {
			Text string `yaml:"prompt_text"`
		}
	}
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Text != want {
		t.Errorf("prompt bytes changed: %+v", got.Tasks)
	}
}
