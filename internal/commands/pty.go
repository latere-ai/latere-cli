// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	cellaclient "latere.ai/x/cella/client"
)

// defaultTerminalCols and defaultTerminalRows are the window a shell asks
// for when the CLI's own output is not a terminal.
const (
	defaultTerminalCols = 80
	defaultTerminalRows = 24
)

func newCeShellCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "shell <name|id> [-- <cmd>...]",
		Aliases: []string{"attach"},
		Short:   "Open an interactive terminal inside a cella.",
		Long: `Open an interactive terminal inside a running cella: the image's shell,
or the command given after --. The terminal follows your window's size,
and the CLI exits with the shell's exit code.

If the cella is stopped, start it first with 'latere cella start'.
The alias 'attach' is kept for users who prefer terminal attachment
language.`,
		Example: `  latere cella shell dev
  latere cella attach dev
  latere cella shell dev -- python3`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			req := cellaclient.ExecRequest{Command: args[1:]}
			req.Cols, req.Rows = terminalWindow()
			session, err := c.AttachSession(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			code, err := driveSession(session, cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return remoteExit(code)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// terminalWindow is the size of the CLI's own terminal, or a fixed window
// when its output is not one.
func terminalWindow() (cols, rows int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		return defaultTerminalCols, defaultTerminalRows
	}
	return cols, rows
}

// driveSession runs one terminal session to its end: the local input goes
// up, the output comes down, the window follows the local one, and the local
// terminal is restored on every exit path. It returns the exit code of the
// command inside.
func driveSession(session *cellaclient.Session, in io.Reader, out io.Writer) (int, error) {
	defer func() { _ = session.Close() }()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fd := int(f.Fd())
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return 0, err
		}
		defer func() { _ = term.Restore(fd, oldState) }()
	}
	if sigs := resizeSignals(); len(sigs) > 0 {
		resized := make(chan os.Signal, 1)
		signal.Notify(resized, sigs...)
		defer signal.Stop(resized)
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case <-done:
					return
				case <-resized:
					if err := session.Resize(terminalWindow()); err != nil {
						return
					}
				}
			}
		}()
	}
	go func() { _, _ = io.Copy(session, in) }()
	_, copyErr := io.Copy(out, session)
	code, err := session.Wait()
	if err != nil {
		return 0, err
	}
	if copyErr != nil {
		return 0, copyErr
	}
	return code, nil
}
