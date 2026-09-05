// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

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

func TestServedConsentDisconnectDoesNotApproveNextCommand(t *testing.T) {
	root := t.TempDir()
	in, input := io.Pipe()
	// Cleanup closes the input before waiting for servers, including on regressions.
	defer in.Close()
	defer input.Close()
	prompts := make(promptEvents, 8)
	decide := promptExecConsent(in, prompts)
	newPeer := func() (net.Conn, sandbox.Provider, <-chan error) {
		c, s := net.Pipe()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- serveHostSandbox(ctx, s, root, decide)
			close(done)
		}()
		t.Cleanup(func() {
			cancel()
			_ = c.Close()
			_ = s.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("consent server did not exit")
			}
		})
		return c, rpc.NewClient(c), done
	}
	firstConn, first, firstDone := newPeer()
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.Exec(t.Context(), "local", sandbox.ExecOptions{Argv: []string{"sh", "-c", "printf first > first.txt"}})
		firstResult <- err
	}()
	select {
	case <-prompts:
	case <-time.After(time.Second):
		t.Fatal("first command prompt missing")
	}
	_ = firstConn.Close()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("disconnected prompt: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("disconnected server remained blocked on terminal approval")
	}
	if err := <-firstResult; err == nil {
		t.Fatal("disconnected request reported success")
	}
	_, second, _ := newPeer()
	secondResult := make(chan error, 1)
	go func() {
		res, err := second.Exec(t.Context(), "local", sandbox.ExecOptions{Argv: []string{"sh", "-c", "printf second > second.txt"}})
		if err == nil && res.ExitCode != 0 {
			err = errors.New("approved command failed")
		}
		secondResult <- err
	}()
	select {
	case <-prompts:
		t.Fatal("new command prompted before the canceled prompt's answer was discarded")
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := io.WriteString(input, "yes\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-prompts:
		if !strings.Contains(text, "second.txt") {
			t.Fatalf("unexpected prompt: %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("second command prompt missing")
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("command ran without its own approval: %s, %v", name, err)
		}
	}
	if _, err := io.WriteString(input, "yes\n"); err != nil {
		t.Fatal(err)
	}
	awaitConsent(t, secondResult, nil)
	if got, err := os.ReadFile(filepath.Join(root, "second.txt")); err != nil || string(got) != "second" {
		t.Fatalf("approved command output = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "first.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command ran: %v", err)
	}
}
