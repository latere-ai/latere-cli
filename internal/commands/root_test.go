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
