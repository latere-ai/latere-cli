// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build !unix

package upgrade

import "errors"

// reExec is unsupported on non-unix platforms (no execve); callers fall back
// to asking the user to restart latere.
func reExec() error {
	return errors.New("re-exec not supported on this platform")
}
