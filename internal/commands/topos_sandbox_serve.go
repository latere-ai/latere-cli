// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"

	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"

	"github.com/latere-ai/latere-cli/internal/tunnel"
)

// SandboxDescriptor is the handshake the edge writes on the control stream when
// it connects a mode-2 sandbox tunnel — it advertises the workspace root it will
// serve. It mirrors the Lux tunnel's Descriptor handshake, on the sandbox tunnel.
type SandboxDescriptor struct {
	NodeID string `json:"node_id"`
	Root   string `json:"root"`
}

// sandboxYamuxConfig mirrors the Lux tunnel's config: keepalive on so a dead peer
// is detected, logs discarded.
func sandboxYamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.EnableKeepAlive = true
	c.KeepAliveInterval = 15 * time.Second
	c.LogOutput = io.Discard
	return c
}

// serveSandboxTunnel connects this machine as a sandbox the control plane drives
// (mode 2): it runs a yamux client over conn, opens a control stream advertising
// the workspace root, then serves every stream the control plane opens as a
// confined+consented Provider-RPC channel against the local workspace
// (serveHostSandbox). conn is a WSS NetConn in production and any net.Conn (a
// localhost TCP link) for local verification — the transport is otherwise opaque.
// It returns when the session ends or ctx is cancelled.
func serveSandboxTunnel(ctx context.Context, conn net.Conn, root string, consent sandbox.ConsentFunc, out io.Writer) error {
	sess, err := yamux.Client(conn, sandboxYamuxConfig())
	if err != nil {
		return fmt.Errorf("sandbox tunnel: yamux: %w", err)
	}
	defer func() { _ = sess.Close() }()

	// The edge opens the control stream (the control plane opens the work
	// streams), matching the Lux tunnel's directionality.
	ctrl, err := sess.OpenStream()
	if err != nil {
		return fmt.Errorf("sandbox tunnel: control stream: %w", err)
	}
	node := sandboxNodeID()
	line, err := json.Marshal(SandboxDescriptor{NodeID: node, Root: root})
	if err != nil {
		return fmt.Errorf("sandbox tunnel: encode descriptor: %w", err)
	}
	if _, err := ctrl.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("sandbox tunnel: write descriptor: %w", err)
	}
	// Echo the machine name: a session binds with edge "" ("my edge") when this
	// is your only connected machine, and by this name when it is not.
	fprintf(out, "sandbox tunnel: connected as %q; serving %s\n", node, root)

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return err // session closed / ctx cancelled
		}
		go func() { _ = serveHostSandbox(ctx, stream, root, consent) }()
	}
}

// sandboxNodeID is the machine name a mode-2 edge advertises when it connects.
// It defaults to the OS hostname (lowercased, domain stripped) so that a caller
// with more than one machine connected picks between meaningful names like
// "changkun-mbp" rather than a random id — while a caller with a single machine
// never needs a name at all (sandbox_node ""). It falls back to the stable random
// tunnel.NodeID() when the hostname is unavailable.
func sandboxNodeID() string {
	if h, err := os.Hostname(); err == nil {
		if id := sanitizeNodeID(h); id != "" {
			return id
		}
	}
	return tunnel.NodeID()
}

func sanitizeNodeID(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.IndexByte(h, '.'); i > 0 { // drop any .local / domain suffix
		h = h[:i]
	}
	return strings.ReplaceAll(h, " ", "-")
}

// serveHostSandbox exposes this machine as a confined, consented sandbox.Provider
// over conn, so a remote (mode-2, interactive-session-modes) session can drive the
// edge's files and commands as its sandbox. It composes the ratified trust
// protections around the local host provider before serving the RPC:
//
//	rpc.Serve(conn, Consent(Confine(hostSandbox(root), root), consent))
//
// so path-root confinement (#1) + the non-overridable secret deny-list (#2) apply
// to every path, and per-call exec consent (#3) prompts before any command runs on
// the real machine. Content withheld from the durable control plane (#4) is the
// control-plane's concern, not the edge's. conn is any bidirectional stream (a
// tunnel stream in production; an in-memory pipe in tests).
func serveHostSandbox(ctx context.Context, conn io.ReadWriteCloser, root string, consent sandbox.ConsentFunc) error {
	host, err := newHostSandbox(root)
	if err != nil {
		return fmt.Errorf("serve sandbox: root %q: %w", root, err)
	}
	provider := sandbox.Consent(sandbox.Confine(host, root), consent)
	return rpc.Serve(ctx, conn, provider)
}

// promptExecConsent is the default edge-side consent decider: it prints the
// command a remote session wants to run and blocks for a y/N answer read from in
// (stdin in production). Any non-yes answer denies. It is the interactive form of
// trust protection #3; a session-scoped "allow all" is a follow-on.
func promptExecConsent(in io.Reader, out io.Writer) sandbox.ConsentFunc {
	return func(_ context.Context, _ string, opts sandbox.ExecOptions) error {
		fprintf(out, "remote session wants to run: %s\nallow? [y/N] ", strings.Join(opts.Argv, " "))
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
