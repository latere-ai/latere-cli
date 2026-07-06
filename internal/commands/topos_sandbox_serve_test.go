// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"
)

// dialServedSandbox wires an rpc client to serveHostSandbox(root, consent) over an
// in-memory pipe (no tunnel/network) and returns the client + a stop func.
func dialServedSandbox(t *testing.T, root string, consent sandbox.ConsentFunc) (sandbox.Provider, func()) {
	t.Helper()
	cConn, sConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		_ = serveHostSandbox(context.Background(), sConn, root, consent)
		close(done)
	}()
	cli := rpc.NewClient(cConn)
	stop := func() {
		if cl, ok := cli.(interface{ Close() error }); ok {
			_ = cl.Close()
		}
		_ = sConn.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return cli, stop
}

// TestServeHostSandboxRoundTrip proves the real laptop provider (hostSandbox)
// composes with the mode-2 stack over the wire: a remote client reads/writes files
// in the workspace root and runs an approved command.
func TestServeHostSandboxRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	allow := func(context.Context, string, sandbox.ExecOptions) error { return nil }
	cli, stop := dialServedSandbox(t, root, allow)
	defer stop()

	sb, err := cli.Create(ctx, sandbox.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cli.WriteFile(ctx, sb.ID, "sub/a.txt", []byte("hello laptop")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := cli.ReadFile(ctx, sb.ID, "sub/a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello laptop" {
		t.Fatalf("ReadFile = %q, want 'hello laptop'", got)
	}
	// The file really landed on disk under the root.
	if b, _ := os.ReadFile(filepath.Join(root, "sub/a.txt")); string(b) != "hello laptop" {
		t.Fatalf("file not written under root: %q", b)
	}
	// An approved exec runs.
	res, err := cli.Exec(ctx, sb.ID, sandbox.ExecOptions{Argv: []string{"sh", "-c", "echo ran"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "ran") {
		t.Fatalf("Exec = %+v, want exit 0 + 'ran'", res)
	}
}

// TestServeHostSandboxEnforcesTrust proves the ratified protections hold over the
// wire against the real host provider: escapes + secrets are refused (ErrConfined)
// and a denied exec is refused (ErrConsentDenied) — none touch the real machine.
func TestServeHostSandboxEnforcesTrust(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// Plant a "secret" and an out-of-root file to prove they stay unreadable.
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	deny := func(context.Context, string, sandbox.ExecOptions) error { return errors.New("no") }
	cli, stop := dialServedSandbox(t, root, deny)
	defer stop()

	sb, _ := cli.Create(ctx, sandbox.CreateOptions{})

	// #1 path-root confinement: an absolute path outside root is refused.
	if _, err := cli.ReadFile(ctx, sb.ID, "/etc/hosts"); !errors.Is(err, sandbox.ErrConfined) {
		t.Fatalf("absolute-outside-root read = %v, want ErrConfined", err)
	}
	// #1 traversal escape refused.
	if _, err := cli.ReadFile(ctx, sb.ID, "../../etc/passwd"); !errors.Is(err, sandbox.ErrConfined) {
		t.Fatalf("traversal read = %v, want ErrConfined", err)
	}
	// #2 secret deny-list: the planted .env is refused even though it is in root.
	if _, err := cli.ReadFile(ctx, sb.ID, ".env"); !errors.Is(err, sandbox.ErrConfined) {
		t.Fatalf("secret read = %v, want ErrConfined", err)
	}
	// #3 per-call consent: a denied exec never runs.
	if _, err := cli.Exec(ctx, sb.ID, sandbox.ExecOptions{Argv: []string{"rm", "-rf", root}}); !errors.Is(err, sandbox.ErrConsentDenied) {
		t.Fatalf("denied exec = %v, want ErrConsentDenied", err)
	}
	// The root still exists (the rm never ran).
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root was touched by a denied exec: %v", err)
	}
}

func TestPromptExecConsent(t *testing.T) {
	var out strings.Builder
	decide := promptExecConsent(strings.NewReader("y\n"), &out)
	if err := decide(context.Background(), "sb", sandbox.ExecOptions{Argv: []string{"ls"}}); err != nil {
		t.Fatalf("a 'y' answer must approve: %v", err)
	}
	if !strings.Contains(out.String(), "allow?") {
		t.Fatalf("prompt not shown: %q", out.String())
	}
	deny := promptExecConsent(strings.NewReader("n\n"), &strings.Builder{})
	if err := deny(context.Background(), "sb", sandbox.ExecOptions{Argv: []string{"ls"}}); err == nil {
		t.Fatal("an 'n' answer must deny")
	}
}
