// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"fmt"
	"io"
	"strings"

	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"
)

// serveHostSandbox exposes this machine as a confined, consented sandbox.Provider
// over conn, so a remote (mode-2, interactive-session-modes) session can drive the
// laptop's files and commands as its sandbox. It composes the ratified trust
// protections around the local host provider before serving the RPC:
//
//	rpc.Serve(conn, Consent(Confine(hostSandbox(root), root), consent))
//
// so path-root confinement (#1) + the non-overridable secret deny-list (#2) apply
// to every path, and per-call exec consent (#3) prompts before any command runs on
// the real machine. Content withheld from the durable control plane (#4) is the
// control-plane's concern, not the laptop's. conn is any bidirectional stream (a
// tunnel stream in production; an in-memory pipe in tests).
func serveHostSandbox(ctx context.Context, conn io.ReadWriteCloser, root string, consent sandbox.ConsentFunc) error {
	host, err := newHostSandbox(root)
	if err != nil {
		return fmt.Errorf("serve sandbox: root %q: %w", root, err)
	}
	provider := sandbox.Consent(sandbox.Confine(host, root), consent)
	return rpc.Serve(ctx, conn, provider)
}

// promptExecConsent is the default laptop-side consent decider: it prints the
// command a remote session wants to run and blocks for a y/N answer read from in
// (stdin in production). Any non-yes answer denies. It is the interactive form of
// trust protection #3; a session-scoped "allow all" is a follow-on.
func promptExecConsent(in io.Reader, out io.Writer) sandbox.ConsentFunc {
	return func(_ context.Context, _ string, opts sandbox.ExecOptions) error {
		fmt.Fprintf(out, "remote session wants to run: %s\nallow? [y/N] ", strings.Join(opts.Argv, " "))
		var answer string
		_, _ = fmt.Fscanln(in, &answer)
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return nil
		default:
			return fmt.Errorf("user declined %q", strings.Join(opts.Argv, " "))
		}
	}
}
