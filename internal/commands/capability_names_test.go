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
		"environments": {"apply", "cat", "delete", "exec", "export", "get", "import", "list", "logs", "ls", "mkdir", "mv", "rm", "run", "shell", "start", "stop", "upload", "write"},
		"agents":       {"provider", "review", "run"},
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
}

// Each retired word is hidden from the help, takes whatever followed it, and
// exits 1 with one sentence naming its replacement, so a script that still
// calls it fails on its first run with the new command in the error.
func TestRetiredCommandWords(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"cella", "list"}, "'latere cella' is now 'latere environments'"},
		{[]string{"app", "list"}, "'latere app' is now 'latere apps'"},
		{[]string{"cella", "apply", "-f", "workload.yaml", "--wait"}, "'latere cella' is now 'latere environments'"},
		{[]string{"sandbox", "ls", "dev", "/workspace"}, "'latere sandbox' is now 'latere environments'"},
		{[]string{"topos", "--local", "-p", "explain this repo"}, "is now 'latere agents run'"},
		{[]string{"topos", "login"}, "'latere topos login' is 'latere agents provider'"},
		{[]string{"review", "--forks", "3"}, "'latere review' is now 'latere agents review'"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			root := NewRoot("test")
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tc.args)
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
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
