package commands

import (
	"fmt"
	"strings"

	"github.com/latere-ai/latere-cli/internal/upgrade"
	"github.com/spf13/cobra"
)

func newUpgradeCmd(version string) *cobra.Command {
	var (
		checkOnly bool
		auto      string
	)
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade latere to the latest release.",
		Long: `Upgrade latere to the latest release published on GitHub.

By default latere checks for new releases at most once a day and prints a
short notice when one is available. Run this command to install the latest
release, or turn on auto-upgrade so latere updates itself the next time it
runs after a new release appears.

The latest release is resolved from github.com/latere-ai/latere-cli, the
archive's checksum is verified, and the running binary is replaced in place.
If latere was installed somewhere you cannot write, re-run install.sh.

Set LATERE_NO_UPDATE_CHECK=1 to silence the passive update check.`,
		Example: `  latere upgrade            # install the latest release
  latere upgrade --check    # report whether a newer release exists
  latere upgrade --auto on  # auto-upgrade on the next run when one is available
  latere upgrade --auto off # stop auto-upgrading`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if auto != "" {
				return setAutoUpgrade(cmd, auto)
			}
			return upgrade.Run(cmd.Context(), version, checkOnly, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for a newer release; do not install.")
	cmd.Flags().StringVar(&auto, "auto", "", "Enable or disable auto-upgrade on the next run (on|off).")
	return cmd
}

// setAutoUpgrade persists the auto-upgrade preference in config.json.
func setAutoUpgrade(cmd *cobra.Command, val string) error {
	cfg := upgrade.LoadConfig()
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "on", "true", "yes", "enable", "enabled":
		cfg.AutoUpgrade = true
	case "off", "false", "no", "disable", "disabled":
		cfg.AutoUpgrade = false
	default:
		return fmt.Errorf("invalid value %q for --auto; use on or off", val)
	}
	if err := upgrade.SaveConfig(cfg); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if cfg.AutoUpgrade {
		fmt.Fprintln(out, "Auto-upgrade enabled. latere will update itself on the next run when a new release is available.")
	} else {
		fmt.Fprintln(out, "Auto-upgrade disabled.")
	}
	return nil
}
