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

// commandExitError requires a confirmed successful exit. A missing status or
// failed lifecycle must not turn into shell success, even if cleanup retained 0.
func commandExitError(state string, code *int) error {
	if code == nil {
		return fmt.Errorf("remote command ended in state %q without an exit code", state)
	}
	if *code < 0 || *code > 255 {
		return fmt.Errorf("remote command ended in state %q with invalid exit code %d", state, *code)
	}
	if *code != 0 {
		return &remoteExitError{code: *code}
	}
	if state != "exited" {
		return fmt.Errorf("remote command ended in state %q despite exit code 0", state)
	}
	return nil
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
