// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"
)

// TestSandboxTunnelOverTCP verifies the mode-2 tunnel transport over a REAL
// network loop on this machine (localhost TCP + yamux), not an in-memory pipe:
// the laptop connects via serveSandboxTunnel and a simulated control plane
// (yamux server) reads the descriptor, opens a Provider-RPC stream, and drives the
// laptop's real workspace — with the ratified trust protections enforced end to
// end. This is the transport a WSS tunnel wraps; the auth handshake is the only
// piece it adds on top.
func TestSandboxTunnelOverTCP(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Laptop side: dial the "control plane" and serve this workspace.
	ctx := t.Context()
	go func() {
		conn, derr := net.Dial("tcp", ln.Addr().String())
		if derr != nil {
			return
		}
		allow := func(context.Context, string, sandbox.ExecOptions) error { return nil }
		_ = serveSandboxTunnel(ctx, conn, root, allow, io.Discard)
	}()

	// Control-plane side: accept the laptop, read its descriptor, then open a
	// Provider-RPC stream and drive the laptop as a sandbox.
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	sess, err := yamux.Server(conn, sandboxYamuxConfig())
	if err != nil {
		t.Fatalf("yamux server: %v", err)
	}
	defer sess.Close()

	ctrl, err := sess.AcceptStream()
	if err != nil {
		t.Fatalf("accept control stream: %v", err)
	}
	var desc SandboxDescriptor
	if err := json.NewDecoder(ctrl).Decode(&desc); err != nil {
		t.Fatalf("decode descriptor: %v", err)
	}
	if desc.Root != root {
		t.Fatalf("descriptor root = %q, want %q", desc.Root, root)
	}

	stream, err := sess.OpenStream()
	if err != nil {
		t.Fatalf("open provider stream: %v", err)
	}
	provider := rpc.NewClient(stream)
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()

	// Drive the real laptop workspace over the tunnel.
	sb, err := provider.Create(dctx, sandbox.CreateOptions{})
	if err != nil {
		t.Fatalf("Create over tunnel: %v", err)
	}
	if err := provider.WriteFile(dctx, sb.ID, "note.txt", []byte("via tunnel")); err != nil {
		t.Fatalf("WriteFile over tunnel: %v", err)
	}
	got, err := provider.ReadFile(dctx, sb.ID, "note.txt")
	if err != nil || string(got) != "via tunnel" {
		t.Fatalf("ReadFile over tunnel = (%q, %v), want 'via tunnel'", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "note.txt")); string(b) != "via tunnel" {
		t.Fatalf("file not written on the real laptop workspace: %q", b)
	}

	// Trust protections hold over the real tunnel: the planted secret and an
	// escape are refused.
	if _, err := provider.ReadFile(dctx, sb.ID, ".env"); !errors.Is(err, sandbox.ErrConfined) {
		t.Fatalf("secret read over tunnel = %v, want ErrConfined", err)
	}
	if _, err := provider.ReadFile(dctx, sb.ID, "../../etc/passwd"); !errors.Is(err, sandbox.ErrConfined) {
		t.Fatalf("escape read over tunnel = %v, want ErrConfined", err)
	}
}
