// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"

	"github.com/spf13/cobra"
)

// errToposHostedRetired is returned when `latere topos` runs without --local:
// the hosted Topos platform is retired, so the local agent is the only mode.
var errToposHostedRetired = errors.New("the hosted Topos platform is retired; run 'latere topos --local' to run an agent on this machine")

// newToposCmd is the `latere topos …` command group: the Topos agent loop run
// on this machine, and the picker for the model provider it uses.
func newToposCmd() *cobra.Command {
	var (
		local bool
		dir   string
		model string
		print string
	)
	cmd := &cobra.Command{
		Use:   "topos",
		Short: "Topos: run the Latere agent on this machine.",
		Long: `Topos is the Latere agent loop.

Run 'latere topos --local' to run an agent entirely on this machine: it works in
your current directory with your real files, using your local model credential
(ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN). No control plane, no login.

The hosted Topos platform is retired, so --local is required.`,
		Example: `  latere topos --local                      run an agent here, on your files
  latere topos --local -p "add a test for foo()"   one-shot, then exit
  latere topos login                        choose the model provider`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !local {
				return errToposHostedRetired
			}
			return runToposLocal(cmd.Context(), dir, model, print, cmd.Root().Version)
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "run the agent on this machine (no control plane), like Claude Code")
	cmd.Flags().StringVar(&dir, "dir", ".", "working directory for --local (default: current directory)")
	cmd.Flags().StringVar(&model, "model", "", "model name for --local (default: the adapter's default)")
	cmd.Flags().StringVarP(&print, "print", "p", "", "with --local: run this one prompt, stream the result, and exit")
	cmd.AddCommand(newToposLoginCmd())
	return cmd
}

// newToposLoginCmd implements `latere topos login`: choose and configure the
// model provider the local agent uses (Claude OAuth, an Anthropic API key, or
// Ollama). Running it explicitly lets you switch providers even when a
// CLAUDE_CODE_OAUTH_TOKEN is in your environment.
func newToposLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Choose the model provider for the local agent (latere topos --local).",
		Long: `Choose and configure the model provider for 'latere topos --local'.

Opens a picker: sign in with Claude (browser, no copy/paste), paste an Anthropic
API key, or use Ollama (local models, no key). The choice is saved to
~/.config/latere/topos-provider.json and takes precedence over any ambient
CLAUDE_CODE_OAUTH_TOKEN, so you can escape Claude Code's shared rate limit by
picking an API key (separate quota) or Ollama (fully local).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthPicker(cmd.Context())
		},
	}
}
