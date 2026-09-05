// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

// interactiveSessionDTO is one row of GET /v1/sessions (and the POST reply).
type interactiveSessionDTO struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type listSessionsResponse struct {
	Sessions []interactiveSessionDTO `json:"sessions"`
}

// newToposSessionStartCmd implements `latere topos session start <agent-id>`:
// it creates an interactive session and either opens the TUI or, with --print,
// runs the prompt non-interactively and streams the result to stdout.
func newToposSessionStartCmd() *cobra.Command {
	var (
		apiURL   string
		print    string
		fromRepo string
	)
	cmd := &cobra.Command{
		Use:   "start <agent-id>",
		Short: "Start an interactive session and attach.",
		Long: `Start a new interactive session on an agent and attach to it.

With no --print, this opens a terminal UI (like a coding assistant): type
messages, watch tools run and tokens stream, approve gated tool calls, and
press Esc to interrupt. Detaching (Ctrl+C) leaves the session running; reattach
later with 'latere topos session attach <id>'.

With --print/-p, it runs one prompt non-interactively and streams the result to
stdout (for scripts and pipelines), then exits — like 'claude -p'.`,
		Example: `  latere topos session start agent_01hxy
  latere topos session start agent_01hxy -p "summarise README.md"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := toposClient(cmd.Context(), apiURL)
			if err != nil {
				return err
			}
			body := map[string]string{"agent_id": args[0]}
			if print != "" {
				body["prompt"] = print
			}
			if fromRepo != "" {
				// Start from a server-side clone of the repo (no laptop files
				// pushed); the server records a lift receipt so a dead sandbox
				// reprovisions by re-cloning on resume.
				body["from_repo"] = fromRepo
			}
			var created interactiveSessionDTO
			if err := c.PostJSON(cmd.Context(), "/v1/sessions", body, &created); err != nil {
				return err
			}
			if print != "" {
				// The session was created with the prompt, so the turn is already
				// running; stream it read-only and exit.
				return runPrintSession(cmd.Context(), c, created.ID, "")
			}
			return runInteractiveSession(cmd.Context(), c, created.ID, false)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	cmd.Flags().StringVarP(&print, "print", "p", "", "non-interactive: run this prompt, stream the result, and exit")
	cmd.Flags().StringVar(&fromRepo, "from-repo", "", "start from a server-side clone of this git repository URL")
	return cmd
}

// newToposSessionAttachCmd implements `latere topos session attach <id>`.
func newToposSessionAttachCmd() *cobra.Command {
	var (
		apiURL   string
		print    string
		readonly bool
	)
	cmd := &cobra.Command{
		Use:   "attach <session-id>",
		Short: "Attach to an existing interactive session.",
		Long: `Attach to a running interactive session by id.

Opens the terminal UI by default. --readonly attaches as a viewer (no input).
With --print/-p, sends one prompt non-interactively, streams the result, and
exits. --readonly cannot be combined with --print.`,
		Example: `  latere topos session attach sess_01hxy
  latere topos session attach sess_01hxy --readonly
  latere topos session attach sess_01hxy -p "now add a test"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if readonly && print != "" {
				return fmt.Errorf("--readonly cannot be combined with --print")
			}
			c, err := toposClient(cmd.Context(), apiURL)
			if err != nil {
				return err
			}
			if print != "" {
				return runPrintSession(cmd.Context(), c, args[0], print)
			}
			return runInteractiveSession(cmd.Context(), c, args[0], readonly)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	cmd.Flags().StringVarP(&print, "print", "p", "", "non-interactive: send this prompt, stream the result, and exit")
	cmd.Flags().BoolVar(&readonly, "readonly", false, "attach as a read-only viewer")
	return cmd
}

// newToposSessionLsCmd implements `latere topos session ls`.
func newToposSessionLsCmd() *cobra.Command {
	var (
		apiURL string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List interactive sessions.",
		Long:  "List the interactive sessions visible to the current token, with their state.",
		Example: `  latere topos session ls
  latere topos session ls --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := toposClient(cmd.Context(), apiURL)
			if err != nil {
				return err
			}
			var resp listSessionsResponse
			if err := c.GetJSON(cmd.Context(), "/v1/sessions", &resp); err != nil {
				return err
			}
			if jsonF {
				return printJSON(resp.Sessions)
			}
			if len(resp.Sessions) == 0 {
				fprintln(os.Stdout, "No interactive sessions.")
				return nil
			}
			for _, s := range resp.Sessions {
				fmt.Fprintf(os.Stdout, "%s  %-16s  %s\n", s.ID, s.Status, s.AgentID) //nolint:errcheck
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// runInteractiveSession opens the bubbletea TUI over an auto-reconnecting attach
// stream.
func runInteractiveSession(ctx context.Context, c *api.Client, sessionID string, readonly bool) error {
	dial := func(ctx context.Context, since int64) (*attachConn, error) {
		return dialAttach(ctx, c.BaseURL, c.Token, sessionID, since, readonly)
	}
	fs := newFrameStream(ctx, dial)
	defer fs.Close()
	p := tea.NewProgram(newTUIModel(sessionID, fs.Events(), fs, readonly), tea.WithContext(ctx), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// runPrintSession streams a session non-interactively to stdout/stderr. When
// turn is non-empty it attaches read-write to send the turn; otherwise it
// attaches read-only (the turn was started at create time).
func runPrintSession(ctx context.Context, c *api.Client, sessionID, turn string) error {
	conn, err := dialAttach(ctx, c.BaseURL, c.Token, sessionID, 0, turn == "")
	if err != nil {
		return err
	}
	defer conn.Close()
	return streamPrint(ctx, conn, os.Stdout, os.Stderr, turn)
}
