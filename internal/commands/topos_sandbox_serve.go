// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"

	"github.com/latere-ai/latere-cli/internal/config"
)

// SandboxDescriptor is the handshake the edge writes on the control stream when
// it connects a mode-2 sandbox tunnel — it advertises the workspace root it will
// serve.
type SandboxDescriptor struct {
	NodeID string `json:"node_id"`
	Root   string `json:"root"`
}

// sandboxYamuxConfig is the tunnel's session config: keepalive on so a dead
// peer is detected, logs discarded.
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
// It closes conn and waits for accepted work streams to stop when the session
// ends or ctx is cancelled. Consent callbacks must honor their context.
func serveSandboxTunnel(parent context.Context, conn net.Conn, root string, consent sandbox.ConsentFunc, out io.Writer) (retErr error) {
	ctx, cancel := context.WithCancel(parent)
	closed := make(chan struct{})
	context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
		_ = conn.Close()
		close(closed)
	})
	var workers sync.WaitGroup
	var sess *yamux.Session
	defer func() {
		cancel()
		<-closed
		if sess != nil {
			_ = sess.Close()
		}
		workers.Wait()
		if parent.Err() != nil {
			retErr = parent.Err()
		}
	}()
	var err error
	sess, err = yamux.Client(conn, sandboxYamuxConfig())
	if err != nil {
		return fmt.Errorf("sandbox tunnel: yamux: %w", err)
	}

	// The edge opens the control stream and the control plane opens the work
	// streams.
	ctrl, err := sess.OpenStream()
	if err != nil {
		return fmt.Errorf("sandbox tunnel: control stream: %w", err)
	}
	node := sandboxNodeID(out)
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
		workers.Go(func() { _ = serveHostSandbox(ctx, stream, root, consent) })
	}
}

// sandboxNodeID is the machine name a mode-2 edge advertises when it connects.
// It defaults to the OS hostname (lowercased, domain stripped) so that a caller
// with more than one machine connected picks between meaningful names like
// "changkun-mbp" rather than a random id — while a caller with a single machine
// never needs a name at all (sandbox_node ""). It falls back to the stable random
// id of persistedNodeID when the hostname is unavailable, and says on out when
// that id cannot be kept.
func sandboxNodeID(out io.Writer) string {
	if h, err := os.Hostname(); err == nil {
		if id := sanitizeNodeID(h); id != "" {
			return id
		}
	}
	id, err := persistedNodeID()
	if err != nil {
		fprintf(out, "note: %v; this machine advertises a new name on each connect\n", err)
	}
	return id
}

// persistedNodeID is a random per-machine id kept in the config dir as
// tunnel-node-id, so a reconnect advertises the same name and replaces this
// machine's earlier registration instead of adding a second one. It answers
// the id even when it cannot be kept, with the error saying why.
func persistedNodeID() (string, error) {
	p := config.Path("tunnel-node-id")
	if p != "" {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return string(b), nil
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "node-unknown", fmt.Errorf("generate a machine id: %w", err)
	}
	id := "node-" + hex.EncodeToString(buf)
	if p == "" {
		return id, fmt.Errorf("no config dir to keep the machine id in")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return id, fmt.Errorf("keep the machine id: %w", err)
	}
	if err := os.WriteFile(p, []byte(id), 0o600); err != nil {
		return id, fmt.Errorf("keep the machine id: %w", err)
	}
	return id, nil
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
	defer func() { _ = conn.Close() }()
	host, err := newHostSandbox(root)
	if err != nil {
		return fmt.Errorf("serve sandbox: root %q: %w", root, err)
	}
	host.fileRoot, err = os.OpenRoot(host.root)
	if err != nil {
		return fmt.Errorf("serve sandbox: open root %q: %w", root, err)
	}
	defer func() { _ = host.fileRoot.Close() }()
	provider := sandbox.Consent(sandbox.Confine(host, host.root), consent)
	return rpc.Serve(ctx, conn, provider)
}
