// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The command groups are named after the platform's capabilities (spec 011):
// `environments` carries every workload command, and `agents` the local agent,
// its provider and the review.
func TestCapabilityCommandTree(t *testing.T) {
	root := NewRoot("test")
	for path, want := range map[string][]string{
		"environments": {"apply", "cat", "delete", "exec", "export", "get", "import", "list", "logs", "ls", "mkdir", "mv", "port-forward", "rm", "run", "secrets", "shell", "start", "stop", "upload", "write"},
		"agents":       {"provider", "review", "run"},
		"evals":        {"candidates", "experiments", "graders", "metrics", "suites", "tasks"},
	} {
		group, _, err := root.Find([]string{path})
		if err != nil || group.Name() != path {
			t.Fatalf("find %s: %v", path, err)
		}
		if group.Hidden {
			t.Errorf("%s is hidden", path)
		}
		var got []string
		for _, sub := range group.Commands() {
			got = append(got, sub.Name())
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s commands = %v, want %v", path, got, want)
		}
	}
	shell, _, err := root.Find([]string{"environments", "attach"})
	if err != nil || shell.Name() != "shell" {
		t.Errorf("environments attach = %v, %v, want the shell alias", shell, err)
	}
	run, _, err := root.Find([]string{"agents", "run"})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"print", "dir", "model"} {
		if run.Flags().Lookup(flag) == nil {
			t.Errorf("agents run has no --%s", flag)
		}
	}
	if run.Flags().Lookup("local") != nil {
		t.Error("agents run still takes --local; running locally is what the command is")
	}
	// The words named after an open core are not commands at all.
	for _, word := range []string{"cella", "sandbox", "topos", "review", "app"} {
		if found, _, _ := root.Find([]string{word}); found != root {
			t.Errorf("latere %s is still a command: %s", word, found.CommandPath())
		}
	}
}

// Each retired word is hidden from the help, takes whatever followed it, and
// exits 1 with one sentence naming its replacement, so a script that still
// calls it fails on its first run with the new command in the error.
func TestRetiredCommandWords(t *testing.T) {
	if !strings.Contains(retiredCommands["eval"], "'latere evals'") {
		t.Errorf("the eval sentence = %q, want it to name latere evals", retiredCommands["eval"])
	}
	for word, sentence := range retiredCommands {
		args := []string{word, "apply", "-f", "suite.yaml", "--dry-run"}
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := NewRoot("test")
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)
			err := root.Execute()
			if err == nil || err.Error() != sentence {
				t.Fatalf("err = %v, want %q", err, sentence)
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			if out.Len() != 0 {
				t.Errorf("printed %q besides the error", out.String())
			}
		})
	}
	help, err := executeForHelp(NewRoot("test"), "--help")
	if err != nil {
		t.Fatal(err)
	}
	for word := range retiredCommands {
		if regexp.MustCompile(`(?m)^\s+` + word + `\s`).MatchString(help) {
			t.Errorf("latere --help lists the retired word %q:\n%s", word, help)
		}
	}
}
