// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"github.com/spf13/cobra"
)

// newAgentsCmd is the `latere agents …` command group of the platform's
// Agents capability (spec 011): the Latere agent run on this machine, the
// model provider it uses, and the adversarial review of a Claude Code
// session. Hosted agents are created and run on the platform, from the
// console and its API; this group is what runs here.
func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Run the Latere agent on this machine, and review a Claude Code session.",
		Long: `Commands of Latere Agents that run on this machine.

'latere agents run' runs the Latere agent in your current directory, on your
real files, with the model provider 'latere agents provider' chose.
'latere agents review' runs an adversarial review of your latest Claude Code
session.

Hosted agents, which run in their own workload with a session you can
follow, are created and run from the console and its API.`,
		Example: `  latere agents run
  latere agents run -p "add a test for foo()"
  latere agents provider
  latere agents review`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newAgentsRunCmd(), newAgentsProviderCmd(), newReviewCmd())
	return cmd
}

// newAgentsRunCmd is `latere agents run`: the agent loop on this machine, in
// a working directory with your real files and a local model credential.
func newAgentsRunCmd() *cobra.Command {
	var (
		dir   string
		model string
		print string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the Latere agent on this machine, on your files.",
		Long: `Run the Latere agent on this machine. It works in the current directory
(or --dir) with your real files, and calls its model through the provider
'latere agents provider' chose: the Latere models with your login, an
Anthropic API key, a Claude sign-in, or Ollama. It needs no workload and no
hosted session.

With -p it runs one prompt, streams the result and exits; without it opens
the agent's terminal interface.`,
		Example: `  latere agents run
  latere agents run -p "add a test for foo()"
  latere agents run --dir ~/code/app`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runToposLocal(cmd.Context(), dir, model, print, cmd.Root().Version)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "working directory (default: current directory)")
	cmd.Flags().StringVar(&model, "model", "", "model name (default: the provider's default)")
	cmd.Flags().StringVarP(&print, "print", "p", "", "run this one prompt, stream the result, and exit")
	return cmd
}

// newAgentsProviderCmd is `latere agents provider`: choose and configure the
// model provider the local agent uses (Claude OAuth, an Anthropic API key,
// or Ollama). Running it explicitly lets you switch providers even when a
// CLAUDE_CODE_OAUTH_TOKEN is in your environment.
func newAgentsProviderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "provider",
		Short: "Choose the model provider for 'latere agents run'.",
		Long: `Choose and configure the model provider for 'latere agents run'.

Opens a picker: sign in with Claude (browser, no copy/paste), paste an Anthropic
API key, or use Ollama (local models, no key). The choice is saved to
~/.config/latere/agent-provider.json and takes precedence over any ambient
CLAUDE_CODE_OAUTH_TOKEN, so you can escape Claude Code's shared rate limit by
picking an API key (separate quota) or Ollama (fully local).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthPicker(cmd.Context())
		},
	}
}
