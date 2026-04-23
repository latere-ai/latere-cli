package commands

import "github.com/spf13/cobra"

func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes (create, list, rename, start, stop, delete).",
	}
	cmd.AddCommand(
		&cobra.Command{Use: "create", Short: "Create a sandbox.", RunE: notImplemented("sandbox create")},
		&cobra.Command{Use: "list", Short: "List your sandboxes.", RunE: notImplemented("sandbox list")},
		&cobra.Command{Use: "get <name|id>", Short: "Get a sandbox by name or id.", Args: cobra.ExactArgs(1), RunE: notImplemented("sandbox get")},
		&cobra.Command{Use: "rename <name|id> <new-name>", Short: "Rename a sandbox.", Args: cobra.ExactArgs(2), RunE: notImplemented("sandbox rename")},
		&cobra.Command{Use: "start <name|id>", Short: "Start a stopped sandbox.", Args: cobra.ExactArgs(1), RunE: notImplemented("sandbox start")},
		&cobra.Command{Use: "stop <name|id>", Short: "Stop a running sandbox.", Args: cobra.ExactArgs(1), RunE: notImplemented("sandbox stop")},
		&cobra.Command{Use: "delete <name|id>", Short: "Delete a sandbox.", Args: cobra.ExactArgs(1), RunE: notImplemented("sandbox delete")},
	)
	return cmd
}

func newExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <name|id> -- <cmd>...",
		Short: "Run a command inside a sandbox.",
		Args:  cobra.MinimumNArgs(2),
		RunE:  notImplemented("exec"),
	}
}
