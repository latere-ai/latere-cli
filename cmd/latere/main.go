// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Command latere is the command-line interface for the Latere product family.
package main

import (
	"os"

	"github.com/latere-ai/latere-cli/internal/commands"
)

// version is set by the release build via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// First, before the commands and the upgrade check: where `latere
	// devices` can run (launcher_unix.go), this binary may have been
	// started again as a sandboxed command's launcher, which never returns.
	runSandboxLauncher()
	root := commands.NewRoot(version)
	if err := root.Execute(); err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
}
