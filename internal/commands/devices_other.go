// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build !darwin && !linux

package commands

import (
	"runtime"

	"github.com/spf13/cobra"

	"latere.ai/x/cella/device"
)

// newDevicesCmd is the devices group where the local driver has no
// sandbox: every word of it says that devices run on macOS and Linux.
func newDevicesCmd(string) *cobra.Command {
	return &cobra.Command{
		Use:                "devices",
		Short:              "Connect this computer so your sessions can work on it (macOS and Linux).",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return device.Fail(device.CodeUnsupported, "this computer runs "+runtime.GOOS, nil)
		},
	}
}
