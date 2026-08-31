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

// HandleExitError maps a root-command error to a process exit code:
//
//   - a completed review debate with unresolved attacks -> 2 (the summary is
//     already on stdout, so nothing is printed here)
//   - any other error -> 1, printed to w
//
// It lives next to the commands so the exit-code policy is shared with main
// without main importing per-command error types.
func HandleExitError(w io.Writer, err error) int {
	if _, ok := errors.AsType[*unresolvedError](err); ok {
		return 2
	}
	fprintln(w, err)
	return 1
}
