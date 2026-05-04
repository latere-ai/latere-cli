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
				"latere auth login",
				"latere cella create --name dev --tier persistent",
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
			name: "auth login",
			args: []string{"auth", "login", "--help"},
			want: []string{
				"latere auth login --personal",
				"latere auth login --no-browser",
				"override Cella API base URL",
			},
		},
		{
			name: "cella create",
			args: []string{"cella", "create", "--help"},
			want: []string{
				"Create a Cella workspace.",
				"latere cella policy list",
				"latere cella create --name dev --tier persistent --disk 10",
				"idle timeout in minutes; omit for account default, 0 disables",
				"named network policy",
			},
		},
		{
			name: "cella policy",
			args: []string{"cella", "policy", "--help"},
			want: []string{
				"List Cella policy profiles visible to the current token.",
				"latere cella create --policy <name>",
				"choose a selectable policy where SIDECAR is \"no\"",
			},
		},
		{
			name: "cella run",
			args: []string{"cella", "run", "--help"},
			want: []string{
				"Run commands in Cella.",
				"latere cella run dev --follow -- make test",
				"one-shot image ref (default Cella base image)",
			},
		},
		{
			name: "cella import",
			args: []string{"cella", "import", "--help"},
			want: []string{
				"Tar archives are extracted.",
				"latere cella import dev --input app.zip --dest /workspace/app",
				"destination dir in the cella",
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
			if tc.name == "cella create" && strings.Contains(got, "default -1") {
				t.Fatalf("help output leaked internal auto-stop sentinel:\n%s", got)
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
