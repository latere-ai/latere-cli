// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRootSilencesCobraErrorPrinting guards against the double-printed
// error users saw on failures: cobra's own `Error: …` line plus
// main.go's `fmt.Fprintln(os.Stderr, err)` produced two identical
// lines. SilenceErrors on the root command suppresses cobra's copy so
// main is the single sink.
func TestRootSilencesCobraErrorPrinting(t *testing.T) {
	root := NewRoot("test")
	if !root.SilenceErrors {
		t.Fatal("root.SilenceErrors = false, want true (cobra would double-print errors with main.go)")
	}

	wantErr := errors.New("boom")
	root.AddCommand(&cobra.Command{
		Use: "fail",
		RunE: func(cmd *cobra.Command, args []string) error {
			return wantErr
		},
	})

	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"fail"})

	if err := root.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("root.Execute err = %v, want %v", err, wantErr)
	}
	if got := stderr.String(); strings.Contains(got, "boom") || strings.Contains(got, "Error:") {
		t.Fatalf("root printed error to stderr: %q (cobra should be silenced)", got)
	}
}

func TestHelpIncludesUserExamples(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root",
			args: []string{"--help"},
			want: []string{
				"latere login",
				"latere environments apply -f workload.yaml",
				"latere agents run",
				"latere completion zsh",
			},
		},
		{
			name: "completion",
			args: []string{"completion", "--help"},
			want: []string{
				"Generate a shell completion script for latere.",
				"latere completion zsh > ~/.zsh/completions/_latere",
				"latere completion fish > ~/.config/fish/completions/latere.fish",
			},
		},
		{
			name: "login",
			args: []string{"login", "--help"},
			want: []string{
				"latere login --personal",
				"latere login --no-browser",
				"override auth base URL",
			},
		},
		{
			name: "environments apply",
			args: []string{"environments", "apply", "--help"},
			want: []string{
				"Create a workload from a declarative manifest",
				"apiVersion: cella.latere.ai/v1beta1",
				"latere environments apply -f workload.yaml --wait",
				"hold the create until the workload runs or fails",
			},
		},
		{
			name: "environments run",
			args: []string{"environments", "run", "--help"},
			want: []string{
				"Run one command in a disposable workload.",
				"latere environments run --ephemeral --rm -- python -c 'print(\"hello\")'",
				"catalog image: base (the default) or gui",
			},
		},
		{
			name: "environments import",
			args: []string{"environments", "import", "--help"},
			want: []string{
				"Tar archives are extracted.",
				"latere environments import dev --input app.zip --dest /workspace/app",
				"destination dir in the workload",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := executeForHelp(NewRoot("test"), tc.args...)
			if err != nil {
				t.Fatalf("help command failed: %v\noutput:\n%s", err, got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("help output missing %q\noutput:\n%s", want, got)
				}
			}
		})
	}
}

func TestCompletionCommandGeneratesScripts(t *testing.T) {
	got, err := executeForHelp(NewRoot("test"), "completion", "fish")
	if err != nil {
		t.Fatalf("completion fish failed: %v", err)
	}
	for _, want := range []string{"complete -c latere", "__latere_perform_completion", "__complete"} {
		if !strings.Contains(got, want) {
			t.Fatalf("completion output missing %q\noutput:\n%s", want, got)
		}
	}
}

func executeForHelp(root *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}
