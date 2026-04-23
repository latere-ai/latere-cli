package commands

import "github.com/spf13/cobra"

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate against auth.latere.ai.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Start the OAuth2 device-code flow and store credentials in the OS keychain.",
		RunE:  notImplemented("auth login"),
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "whoami",
		Short: "Print the current principal.",
		RunE:  notImplemented("auth whoami"),
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Revoke the refresh token and clear the keychain.",
		RunE:  notImplemented("auth logout"),
	})
	return cmd
}
