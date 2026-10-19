// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Command latere is the command-line interface for the Latere product family.
package main

import (
	"os"

	"latere.ai/x/pkg/hostsandbox"

	"github.com/latere-ai/latere-cli/internal/commands"
)

// version is set by the release build via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// First, before the commands and the upgrade check: `latere devices`
	// hosts Cella's local runtime, which on macOS confines a session's
	// command with Seatbelt and, for a command with a network allowlist,
	// starts this binary again as the command's launcher, which serves its
	// proxy and never returns from Main. Calling it is also what makes the
	// runtime pick Seatbelt on macOS.
	hostsandbox.Main()
	root := commands.NewRoot(version)
	if err := root.Execute(); err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
}
