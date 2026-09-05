// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"latere.ai/x/topos/sandbox"
)

// promptExecConsent serializes terminal prompts and requires a whole-line yes.
// Cancellation returns immediately, but a displayed prompt retains ownership of
// its pending input line until that line arrives. This prevents a late answer
// from approving a different request. At most one input worker remains blocked.
func promptExecConsent(in io.Reader, out io.Writer) sandbox.ConsentFunc {
	gate := make(chan struct{}, 1)
	reader := bufio.NewReader(in)
	return func(ctx context.Context, _ string, opts sandbox.ExecOptions) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		opts.Argv = slices.Clone(opts.Argv)
		opts.Env = maps.Clone(opts.Env)
		opts.SecretEnv = maps.Clone(opts.SecretEnv)
		result := make(chan error, 1)
		go func() {
			defer func() { <-gate }()
			if err := ctx.Err(); err != nil {
				result <- err
				return
			}
			var prompt strings.Builder
			fmt.Fprintf(&prompt, "remote session wants to run: %q\n", opts.Argv)
			if opts.Cwd != "" {
				fmt.Fprintf(&prompt, "working directory: %q\n", opts.Cwd)
			}
			// Only names are shown: environment values may contain credentials.
			if len(opts.Env) > 0 {
				fmt.Fprintf(&prompt, "environment overrides (values hidden): %q\n", slices.Sorted(maps.Keys(opts.Env)))
			}
			if len(opts.SecretEnv) > 0 {
				fmt.Fprintf(&prompt, "secret environment variables: %q\n", slices.Sorted(maps.Keys(opts.SecretEnv)))
			}
			prompt.WriteString("allow? [y/N] ")
			if _, err := io.WriteString(out, prompt.String()); err != nil {
				result <- fmt.Errorf("show command consent: %w", err)
				return
			}
			answer, err := reader.ReadString('\n')
			if ctx.Err() != nil {
				result <- ctx.Err()
				return
			}
			if err != nil && !errors.Is(err, io.EOF) {
				result <- fmt.Errorf("read command consent: %w", err)
				return
			}
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "y", "yes":
				result <- nil
			default:
				result <- fmt.Errorf("user declined %q", opts.Argv)
			}
		}()
		select {
		case err := <-result:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
