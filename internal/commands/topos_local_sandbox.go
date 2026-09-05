// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
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
	// Set only by serve-sandbox; local interactive execution uses host paths.
	fileRoot *os.Root
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

// ResolvePath lets Confine check symlink targets using the served root handle,
// including when the workspace directory has been renamed after startup.
func (h *hostSandbox) ResolvePath(_ context.Context, _, path string) (string, error) {
	if h.fileRoot == nil {
		return h.resolve(path), nil
	}
	rel, err := h.servedPath(path)
	if err != nil {
		return "", err
	}
	rel, err = sandbox.ResolveRootPath(h.fileRoot, rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(h.root, rel), nil
}

func (h *hostSandbox) Create(_ context.Context, opts sandbox.CreateOptions) (sandbox.Sandbox, error) {
	return sandbox.Sandbox{ID: "local", Name: opts.Name, State: sandbox.StateRunning, Tier: "local"}, nil
}

// Destroy is a no-op: the workspace is your real directory, never deleted.
func (h *hostSandbox) Destroy(_ context.Context, _ string) error { return nil }

func (h *hostSandbox) Exec(ctx context.Context, _ string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	if len(opts.Argv) == 0 {
		return sandbox.ExecResult{}, errors.New("host sandbox: exec: argv is empty")
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
		if ctx.Err() != nil {
			res.Phase = "killed"
		}
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
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
	if h.fileRoot != nil {
		rel, err := h.servedPath(path)
		if err != nil {
			return nil, err
		}
		return h.fileRoot.ReadFile(rel)
	}
	return os.ReadFile(h.resolve(path))
}

func (h *hostSandbox) WriteFile(_ context.Context, _, path string, data []byte) error {
	if h.fileRoot != nil {
		rel, err := h.servedPath(path)
		if err != nil {
			return err
		}
		if err := h.fileRoot.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return err
		}
		return h.fileRoot.WriteFile(rel, data, 0o644)
	}
	full := h.resolve(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func (h *hostSandbox) ListFiles(_ context.Context, _, path string) ([]sandbox.FileInfo, error) {
	var entries []fs.DirEntry
	var err error
	if h.fileRoot != nil {
		rel, pathErr := h.servedPath(path)
		if pathErr != nil {
			return nil, pathErr
		}
		entries, err = fs.ReadDir(h.fileRoot.FS(), filepath.ToSlash(rel))
	} else {
		entries, err = os.ReadDir(h.resolve(path))
	}
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

// servedPath preserves absolute paths within the advertised root while making
// every file operation relative to its open directory handle. Root's operations
// enforce containment even if a symlink changes after this lexical check.
func (h *hostSandbox) servedPath(path string) (string, error) {
	rel, err := filepath.Rel(h.root, h.resolve(path))
	if err != nil || !filepath.IsLocal(rel) {
		return "", sandbox.ErrConfined
	}
	return rel, nil
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
