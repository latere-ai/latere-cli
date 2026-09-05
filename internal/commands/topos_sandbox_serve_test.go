// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"errors"
	"io"
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

func TestServeHostSandboxRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	outsideFile := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(outsideFile, []byte("outside data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	client, stop := dialServedSandbox(t, root, nil)
	defer stop()
	ctx := t.Context()
	sb, err := client.Create(ctx, sandbox.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadFile(ctx, sb.ID, "escape/note.txt"); err == nil {
		t.Error("read escaped the served workspace")
	}
	if err := client.WriteFile(ctx, sb.ID, "escape/note.txt", []byte("overwritten")); err == nil {
		t.Error("write escaped the served workspace")
	}
	if _, err := client.ListFiles(ctx, sb.ID, "escape"); err == nil {
		t.Error("listing escaped the served workspace")
	}
	if got, err := os.ReadFile(outsideFile); err != nil || string(got) != "outside data" {
		t.Fatalf("outside file = %q, %v", got, err)
	}
}

func TestServeHostSandboxRelativeRootAndInternalSymlink(t *testing.T) {
	t.Chdir(t.TempDir())
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("real", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", "alias"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	client, stop := dialServedSandbox(t, ".", nil)
	defer stop()
	ctx := t.Context()
	sb, err := client.Create(ctx, sandbox.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "alias", "sub", "note.txt")
	if err := client.WriteFile(ctx, sb.ID, path, []byte("inside")); err != nil {
		t.Fatal(err)
	}
	if got, err := client.ReadFile(ctx, sb.ID, path); err != nil || string(got) != "inside" {
		t.Fatalf("read absolute path = %q, %v", got, err)
	}
	entries, err := client.ListFiles(ctx, sb.ID, "alias/sub")
	if err != nil || len(entries) != 1 || entries[0].Name != "note.txt" {
		t.Fatalf("list through internal alias = %+v, %v", entries, err)
	}
}

func TestServeHostSandboxPinsDirectoryAcrossReplacement(t *testing.T) {
	base := t.TempDir()
	root, moved, outside := filepath.Join(base, "root"), filepath.Join(base, "moved"), filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(filepath.Base(dir)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client, stop := dialServedSandbox(t, root, nil)
	defer stop()
	ctx := t.Context()
	sb, err := client.Create(ctx, sandbox.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, err := client.ReadFile(ctx, sb.ID, "note.txt"); err != nil || string(got) != "root" {
		t.Fatalf("read after root replacement = %q, %v", got, err)
	}
	if err := client.WriteFile(ctx, sb.ID, "note.txt", []byte("updated")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(moved, "note.txt")); err != nil || string(got) != "updated" {
		t.Fatalf("original directory = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "note.txt")); err != nil || string(got) != "outside" {
		t.Fatalf("outside directory = %q, %v", got, err)
	}
}

func TestBoundHostSandboxRejectsOutsidePaths(t *testing.T) {
	host, err := newHostSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host.fileRoot, err = os.OpenRoot(host.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.fileRoot.Close() })
	outside := t.TempDir()
	path := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(path, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := host.ReadFile(ctx, "local", path); !errors.Is(err, sandbox.ErrConfined) {
		t.Errorf("bound read = %v, want ErrConfined", err)
	}
	if err := host.WriteFile(ctx, "local", path, []byte("overwrite")); !errors.Is(err, sandbox.ErrConfined) {
		t.Errorf("bound write = %v, want ErrConfined", err)
	}
	if _, err := host.ListFiles(ctx, "local", outside); !errors.Is(err, sandbox.ErrConfined) {
		t.Errorf("bound list = %v, want ErrConfined", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "outside" {
		t.Fatalf("outside file = %q, %v", got, err)
	}
}

func TestServeHostSandboxInvalidRootClosesStream(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "missing")
	if err := serveHostSandbox(t.Context(), server, root, nil); err == nil {
		t.Fatal("missing root accepted")
	}
	var data [1]byte
	if _, err := client.Read(data[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("failed server left stream open: %v", err)
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
