// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"latere.ai/x/topos/sandbox"
)

func TestHostExecCancellationProcess(t *testing.T) {
	marker := os.Getenv("LATERE_TEST_EXEC_MARKER")
	if marker == "" {
		return
	}
	_, _ = os.Stdout.WriteString("output before cancellation\n")
	_, _ = os.Stderr.WriteString("stderr before cancellation\n")
	if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
}

func TestHostSandboxReportsCanceledCommands(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "exec"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sb, err := newHostSandbox(dir)
			if err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "started")
			opts := sandbox.ExecOptions{
				Argv: []string{binary, "-test.run=^TestHostExecCancellationProcess$"},
				Env:  map[string]string{"LATERE_TEST_EXEC_MARKER": marker},
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type outcome struct {
				result sandbox.ExecResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				if !streaming {
					res, err := sb.Exec(ctx, "local", opts)
					done <- outcome{res, err}
					return
				}
				stream, err := sb.StreamExec(ctx, "local", opts)
				if err != nil {
					done <- outcome{err: err}
					return
				}
				defer func() { _ = stream.Close() }()
				for {
					_, err = stream.Recv()
					if err != nil {
						break
					}
				}
				if errors.Is(err, io.EOF) {
					err = nil
				}
				done <- outcome{stream.Result(), err}
			}()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("child process did not start")
				}
			}
			cancel()
			select {
			case got := <-done:
				if got.err != nil || got.result.Phase != "killed" {
					t.Fatalf("canceled command = %+v, %v; want killed", got.result, got.err)
				}
				if !strings.Contains(string(got.result.Stdout), "output before cancellation") ||
					!strings.Contains(string(got.result.Stderr), "stderr before cancellation") {
					t.Fatalf("partial output lost: %+v", got.result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled child did not stop")
			}
		})
	}
}

func TestServedHostSandboxRejectsEmptyCommand(t *testing.T) {
	allow := func(context.Context, string, sandbox.ExecOptions) error { return nil }
	client, stop := dialServedSandbox(t, t.TempDir(), allow)
	defer stop()
	ctx := t.Context()
	sb, err := client.Create(ctx, sandbox.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec(ctx, sb.ID, sandbox.ExecOptions{}); err == nil {
		t.Fatal("empty command reported success over RPC")
	}
	stream, err := client.StreamExec(ctx, sb.ID, sandbox.ExecOptions{})
	if stream != nil {
		_ = stream.Close()
	}
	if err == nil {
		t.Fatal("empty streaming command reported success over RPC")
	}
}
