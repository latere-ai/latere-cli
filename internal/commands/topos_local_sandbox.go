// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"latere.ai/x/topos/sandbox"
)

// hostSandbox is a sandbox.Provider that runs directly on the local machine,
// rooted at a working directory (your project), with no isolation. It is what
// `latere topos --local` uses so the agent reads, edits, and runs commands
// against your real files — the same model as Claude Code. The control-plane
// path uses Cella instead; this is deliberately unsandboxed local execution.
type hostSandbox struct {
	root string // absolute working directory the agent operates in
}

func newHostSandbox(root string) (*hostSandbox, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &hostSandbox{root: abs}, nil
}

// resolve maps a tool-supplied path to a real filesystem path: absolute paths
// are used as-is; relative paths are taken against the working directory.
func (h *hostSandbox) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(h.root, path)
}

func (h *hostSandbox) Create(_ context.Context, opts sandbox.CreateOptions) (sandbox.Sandbox, error) {
	return sandbox.Sandbox{ID: "local", Name: opts.Name, State: sandbox.StateRunning, Tier: "local"}, nil
}

// Destroy is a no-op: the workspace is your real directory, never deleted.
func (h *hostSandbox) Destroy(_ context.Context, _ string) error { return nil }

func (h *hostSandbox) Exec(ctx context.Context, _ string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	if len(opts.Argv) == 0 {
		return sandbox.ExecResult{}, nil
	}
	cmd := exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = h.root
	if opts.Cwd != "" {
		cmd.Dir = h.resolve(opts.Cwd)
	}
	cmd.Env = os.Environ()
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := sandbox.ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Phase: "exited"}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil // a non-zero exit is a result, not a provider error
		}
		return res, err // genuine failure to run (e.g. binary not found)
	}
	return res, nil
}

// StreamExec runs the command to completion and returns a one-shot stream. The
// builtin tools use Exec, so a simple non-streaming implementation suffices.
func (h *hostSandbox) StreamExec(ctx context.Context, id string, opts sandbox.ExecOptions) (sandbox.ExecStream, error) {
	res, err := h.Exec(ctx, id, opts)
	if err != nil {
		return nil, err
	}
	return &doneStream{res: res}, nil
}

func (h *hostSandbox) ReadFile(_ context.Context, _, path string) ([]byte, error) {
	return os.ReadFile(h.resolve(path))
}

func (h *hostSandbox) WriteFile(_ context.Context, _, path string, data []byte) error {
	full := h.resolve(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func (h *hostSandbox) ListFiles(_ context.Context, _, path string) ([]sandbox.FileInfo, error) {
	entries, err := os.ReadDir(h.resolve(path))
	if err != nil {
		return nil, err
	}
	out := make([]sandbox.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		out = append(out, sandbox.FileInfo{
			Name: e.Name(), Size: info.Size(), Mode: uint32(info.Mode().Perm()), IsDir: e.IsDir(),
		})
	}
	return out, nil
}

func (h *hostSandbox) HealthCheck(_ context.Context, _ string) error { return nil }

// doneStream is an already-finished ExecStream: it yields the full output once,
// then io.EOF.
type doneStream struct {
	res  sandbox.ExecResult
	done bool
}

func (s *doneStream) Recv() ([]byte, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return append(append([]byte{}, s.res.Stdout...), s.res.Stderr...), nil
}
func (s *doneStream) Result() sandbox.ExecResult { return s.res }
func (s *doneStream) Close() error               { return nil }
