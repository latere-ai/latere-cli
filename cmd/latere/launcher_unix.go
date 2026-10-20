// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package main

import "latere.ai/x/pkg/hostsandbox"

// runSandboxLauncher hands the process to hostsandbox when it was started
// as a sandboxed command's launcher. `latere devices` hosts Cella's local
// runtime, which on macOS confines a session's command with Seatbelt and,
// for a command with a network allowlist, starts this binary again to
// serve the command's proxy; that process never returns from Main. Calling
// it is also what makes the runtime pick Seatbelt on macOS.
func runSandboxLauncher() { hostsandbox.Main() }
