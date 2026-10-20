// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build !(darwin || linux)

package main

// runSandboxLauncher does nothing where `latere devices` does not run: no
// sandboxed command starts this binary again there.
func runSandboxLauncher() {}
