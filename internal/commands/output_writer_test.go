// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import "bytes"

// failingWriter is a command's output that fails: with err nil it keeps
// everything written, and with err set it keeps the next remaining bytes
// and then fails every write with err, as a closed pipe or a full disk
// does part way through a command's output.
type failingWriter struct {
	buffer    bytes.Buffer
	remaining int
	err       error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.err == nil {
		return w.buffer.Write(p)
	}
	n := min(len(p), w.remaining)
	_, _ = w.buffer.Write(p[:n])
	w.remaining -= n
	return n, w.err
}

func (w *failingWriter) String() string { return w.buffer.String() }
