// print.go holds this package's output helpers.
//
// A CLI writes most of what it produces to a stream it cannot recover from.
// stdout is a pipe as often as a terminal, and a write that fails there fails
// because the reader is gone -- `latere drive ls | head` closes the pipe
// mid-listing. There is nowhere left to report that, and the shell already
// knows. Checking every call would put a branch nobody can take on every line
// of output; discarding it at each call site would be indistinguishable from
// having forgotten to look.
//
// So the decision is written down once, here, and the call sites read as
// output rather than as error handling. Anything whose failure a caller can
// act on -- writing a file, a response body, a wire frame -- uses fmt.Fprint*
// directly and checks the error.

package commands

import (
	"fmt"
	"io"
)

// fprintf is fmt.Fprintf with the write error discarded.
func fprintf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

// fprintln is fmt.Fprintln with the write error discarded.
func fprintln(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
