// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/upgrade"
)

func newUpgradeCmd(version string) *cobra.Command {
	var (
		checkOnly bool
		auto      string
	)
	cmd := &cobra.Command{
		Use:   "upgrade [version]",
		Short: "Upgrade, downgrade, or reinstall latere.",
		Long: `Install a latere release published on GitHub. With no argument this
installs the latest release; pass a version (e.g. v0.2.29) to install that
exact release, which is how you roll back if an upgrade turns out to be
broken.

Auto-upgrade is on by default: before each command you run in a terminal,
latere checks for a new release and, when there is one, updates itself
first. Turn it off with --auto off, after which latere checks once a day
and prints a notice instead; if a release is
bad, roll back with 'latere upgrade <previous-version>' and (optionally)
'latere upgrade --auto off' to stay put.

The release is resolved from github.com/latere-ai/latere-cli. Its signature
is checked against the Latere keys built into latere, then the archive's
checksum, and only then is the running binary replaced in place. A release
that is not signed by Latere is refused and nothing changes; releases before
v0.21.0 carry no signature, so latere cannot go back to one. If latere was
installed somewhere you cannot write, run the installer again:
curl -fsSL https://platform.latere.ai | sh

Set LATERE_NO_UPDATE_CHECK=1 to turn the release check off.`,
		Example: `  latere upgrade            # install the latest release
  latere upgrade v0.2.29    # roll back to a specific release
  latere upgrade --check    # report whether a newer release exists
  latere upgrade --auto off # stop auto-upgrading
  latere upgrade --auto on  # re-enable auto-upgrade`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("auto") {
				if checkOnly {
					return fmt.Errorf("--auto cannot be combined with --check")
				}
				if len(args) > 0 {
					return fmt.Errorf("--auto cannot be combined with a version argument")
				}
				return setAutoUpgrade(cmd, auto)
			}
			target := ""
			if len(args) == 1 {
				target = args[0]
				if strings.TrimSpace(target) == "" {
					return fmt.Errorf("invalid version %q; expected a release like v0.2.29", target)
				}
			}
			return upgrade.Run(cmd.Context(), version, target, checkOnly, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for a newer release; do not install.")
	cmd.Flags().StringVar(&auto, "auto", "", "Enable or disable auto-upgrade on the next run (on|off).")
	return cmd
}

// setAutoUpgrade persists the auto-upgrade preference in config.json.
func setAutoUpgrade(cmd *cobra.Command, val string) error {
	var enabled bool
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "on", "true", "yes", "enable", "enabled":
		enabled = true
	case "off", "false", "no", "disable", "disabled":
		enabled = false
	default:
		return fmt.Errorf("invalid value %q for --auto; use on or off", val)
	}
	cfg := upgrade.LoadConfig()
	cfg.AutoUpgrade = &enabled
	if err := upgrade.SaveConfig(cfg); err != nil {
		return err
	}
	message := "Auto-upgrade disabled."
	if enabled {
		message = "Auto-upgrade enabled. latere will update itself on the next run when a new release is available."
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), message); err != nil {
		return fmt.Errorf("write auto-upgrade confirmation (preference was saved): %w", err)
	}
	return nil
}
