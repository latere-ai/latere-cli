package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewRoot builds the full command tree. Version is injected from main.
//
// The root command is product-neutral: each Latere product gets its own
// subcommand group under this binary (sandbox today; more to follow).
func NewRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:          "latere",
		Short:        "Command-line interface for the Latere product family.",
		Long:         "latere is a single binary for interacting with Latere services from the terminal.\nSee https://latere.ai for the product family.",
		SilenceUsage: true,
	}
	root.Version = version
	root.SetVersionTemplate("latere {{.Version}}\n")

	root.AddCommand(newAuthCmd())
	root.AddCommand(newSandboxCmd())
	root.AddCommand(newExecCmd())
	return root
}

// notImplemented is the standard stub body for commands whose backing API is
// not live yet. Keeps the CLI surface honest while scaffolding lands first.
func notImplemented(name string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("%s: not implemented yet. Track progress at https://latere.ai", name)
	}
}
