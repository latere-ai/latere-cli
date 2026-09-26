// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
)

// unresolvedError is returned by `latere review` when a debate completes with
// open attacks. It carries the count so main can map it to a distinct exit
// code: a completed debate with unresolved attacks is the verdict "review
// found issues", which must be distinguishable from a command error. This
// gives adversarial review an exit-code-as-verdict contract.
type unresolvedError struct{ n int }

func (e *unresolvedError) Error() string {
	return fmt.Sprintf("%d unresolved attack(s)", e.n)
}

type remoteExitError struct{ code int }

func (e *remoteExitError) Error() string {
	return fmt.Sprintf("remote command exited with code %d", e.code)
}

// remoteExit is the result of a remote command that ended with code: nil for
// zero, and otherwise the code, which HandleExitError makes this process's
// own. A code outside 0-255 is no process exit status and is reported, never
// passed on as success or truncated into another code.
func remoteExit(code int) error {
	switch {
	case code == 0:
		return nil
	case code < 0 || code > 255:
		return fmt.Errorf("remote command ended with invalid exit code %d", code)
	default:
		return &remoteExitError{code: code}
	}
}

// HandleExitError maps a root-command error to a process exit code:
//
//   - a remote command's nonzero exit code -> that code, without extra output
//   - a completed review debate with unresolved attacks -> 2 (the summary is
//     already on stdout, so nothing is printed here)
//   - any other error -> 1, printed to w
//
// It lives next to the commands so the exit-code policy is shared with main
// without main importing per-command error types.
func HandleExitError(w io.Writer, err error) int {
	if remote, ok := errors.AsType[*remoteExitError](err); ok {
		return remote.code
	}
	if _, ok := errors.AsType[*unresolvedError](err); ok {
		return 2
	}
	fprintln(w, err)
	return 1
}
