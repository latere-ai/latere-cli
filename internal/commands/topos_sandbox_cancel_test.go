// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"
)

func TestServeHostSandboxDisconnectStopsCommand(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "Exec"
		if streaming {
			name = "StreamExec"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, "started")
			c, s := net.Pipe()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			allow := func(context.Context, string, sandbox.ExecOptions) error { return nil }
			go func() {
				done <- serveHostSandbox(ctx, s, root, allow)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				_ = c.Close()
				_ = s.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("server did not stop during cleanup")
				}
			})
			client := rpc.NewClient(c)
			opts := sandbox.ExecOptions{
				Argv: []string{binary, "-test.run=^TestHostExecCancellationProcess$"},
				Env:  map[string]string{"LATERE_TEST_EXEC_MARKER": marker},
			}
			callDone := make(chan error, 1)
			go func() {
				if streaming {
					stream, err := client.StreamExec(ctx, "local", opts)
					if stream != nil {
						_ = stream.Close()
					}
					callDone <- err
				} else {
					_, err := client.Exec(ctx, "local", opts)
					callDone <- err
				}
			}()
			started := time.NewTicker(10 * time.Millisecond)
			defer started.Stop()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				select {
				case <-started.C:
				case err := <-callDone:
					t.Fatalf("command stopped before signaling ready: %v", err)
				case <-deadline.C:
					t.Fatal("command did not start")
				}
			}
			_ = c.Close()
			// serveHostSandbox cannot return until exec.Cmd.Run has waited for the
			// real child to exit. Its parent context intentionally remains active.
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("peer EOF = %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("disconnected session left its command running")
			}
			if ctx.Err() != nil {
				t.Fatal("test canceled server's parent context")
			}
			select {
			case err := <-callDone:
				if err == nil {
					t.Fatal("disconnected command reported success")
				}
			case <-time.After(time.Second):
				t.Fatal("client remained blocked")
			}
		})
	}
}
