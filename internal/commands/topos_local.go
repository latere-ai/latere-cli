// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"latere.ai/x/topos"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// modelNamer is implemented by model adapters that can report the model id they
// request (the Anthropic adapter does), so the REPL can show it.
type modelNamer interface{ Model() string }

// modelString returns the model id a brain requests, or "unknown".
func modelString(m models.Model) string {
	if mn, ok := m.(modelNamer); ok {
		return mn.Model()
	}
	return "unknown"
}

// isInteractiveTTY reports whether both stdin and stdout are terminals, so the
// full-screen TUI can take over the screen (piped/redirected runs use the REPL).
func isInteractiveTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// quietSDKLogs silences the SDK's INFO chatter (e.g. "loop: turn completed")
// that otherwise interleaves with the chat. The loop logs through slog.Default;
// warnings and errors are kept. Genuine failures still surface as returned
// errors and through the Observer.
func quietSDKLogs() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
}

// localSystemPrompt is the default instruction for `latere topos --local`: a
// coding assistant working directly in the user's directory.
const localSystemPrompt = `You are a coding assistant running on the user's local machine, in their working directory.
Use the tools to read, search, edit, and run commands against their real files. Prefer read_file/edit_file/write_file for file changes and bash for commands. Be concise.`

// runToposLocal runs the agent loop entirely on this machine, like Claude Code:
// the model is your local Claude credential, the workspace is your directory, and
// tools execute directly on your files. No control plane, no Cella, no login.
// With oneShot set it runs a single prompt and exits; otherwise it is a REPL.
func runToposLocal(ctx context.Context, dir, modelName, oneShot, version string) error {
	quietSDKLogs()
	brain, err := buildLocalModel(ctx, modelName)
	if errors.Is(err, errNeedAuth) {
		// No credential yet. In an interactive terminal, show the provider picker
		// and retry; otherwise (one-shot -p, or piped) fail with a clear message
		// rather than launching a TUI with no TTY.
		if oneShot != "" || !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("no model credential — run `latere topos login`, or set ANTHROPIC_API_KEY")
		}
		if perr := runAuthPicker(ctx); perr != nil {
			return perr
		}
		brain, err = buildLocalModel(ctx, modelName)
	}
	if err != nil {
		return err
	}
	sb, err := newHostSandbox(dir)
	if err != nil {
		return err
	}
	const sandboxID = "local"

	// Interactive on a real terminal → the full-screen TUI. One-shot (-p) and
	// piped/non-TTY input fall through to the scriptable line REPL below.
	if oneShot == "" && isInteractiveTTY() {
		cwd := dir
		if a, e := filepath.Abs(dir); e == nil {
			cwd = a
		}
		return runLocalTUI(ctx, version, cwd, sb, tools.Builtins(), brain)
	}

	render := &localRenderer{}
	// The runner is rebuilt when the model changes (/model), so it lives in a
	// closure variable; curModel tracks what the header/status shows.
	var runner *topos.Runner
	var curModel string
	rebuild := func(b models.Model) error {
		rn, e := topos.NewRunner(topos.Options{Brain: b, Sandbox: sb, Observer: render.render})
		if e != nil {
			return e
		}
		runner, curModel = rn, modelString(b)
		return nil
	}
	if err := rebuild(brain); err != nil {
		return err
	}

	builtins := tools.Builtins()
	var transcript []models.Message
	turn := func(prompt string) error {
		res, terr := runner.Turn(ctx, topos.TurnInput{
			Sandbox:           sb,
			SandboxID:         sandboxID,
			SystemPrompt:      localSystemPrompt,
			Tools:             builtins,
			InitialTranscript: transcript,
			UserPrompt:        prompt,
		})
		if terr != nil {
			return terr
		}
		transcript = res.Transcript
		render.endTurn()
		return nil
	}

	if oneShot != "" {
		return turn(oneShot)
	}

	abs, _ := os.Getwd()
	fmt.Printf("Topos (local) in %s\n", abs)
	fmt.Printf("model: %s   ·   /model to switch, /help for commands, Ctrl+D to quit\n", curModel)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print("\n› ")
		if !sc.Scan() {
			fmt.Println()
			return nil
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if quit := handleLocalCommand(ctx, line, &curModel, rebuild); quit {
				return nil
			}
			continue
		}
		if err := turn(line); err != nil {
			render.closeText()
			fmt.Fprintln(os.Stderr, styleErr.Render("error: "+err.Error()))
		}
	}
}

// Block styles for the local REPL: a green dot leads an assistant message, a
// blue dot leads a tool action, and results hang under a dim branch (red on
// error) — visually separating message / action / result blocks.
var (
	styleAsstDot  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleToolDot  = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	styleToolName = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// localRenderer streams a run to the terminal as distinct blocks. It keeps the
// small amount of state needed to know when an assistant text block is open, so
// text, tool calls, and results are cleanly separated.
type localRenderer struct {
	inText bool // an assistant text block is currently streaming
}

func (r *localRenderer) render(e topos.Event) {
	switch e.Name {
	case topos.EventTextDelta:
		var p textDeltaPayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil || p.Text == "" {
			return
		}
		if !r.inText {
			fmt.Print("\n" + styleAsstDot.Render("⏺") + " ")
			r.inText = true
		}
		fmt.Print(p.Text)
	case "PreToolUse":
		var p preToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil {
			return
		}
		r.closeText()
		fmt.Printf("\n%s %s%s\n", styleToolDot.Render("⏺"),
			styleToolName.Render(p.ToolCall.Name), styleDim.Render(summarizeToolInput(p.ToolCall.Input)))
	case topos.EventPostToolUse:
		var p postToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil {
			return
		}
		line := truncLine(p.Result.Content, 120)
		if p.Result.IsError {
			fmt.Printf("%s%s\n", styleDim.Render("  ⎿ "), styleErr.Render(line))
			return
		}
		fmt.Printf("%s%s\n", styleDim.Render("  ⎿ "), styleDim.Render(line))
	}
}

// closeText ends an open assistant text block with a newline.
func (r *localRenderer) closeText() {
	if r.inText {
		fmt.Println()
		r.inText = false
	}
}

// endTurn terminates the current turn's output before the next prompt.
func (r *localRenderer) endTurn() { r.closeText() }

// summarizeToolInput renders a compact "(...)" hint from a tool call's input —
// the most salient argument (command, path, pattern) — so an action block reads
// like "⏺ bash (go build ./...)" without dumping the whole JSON.
func summarizeToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query"} {
		if v, ok := m[k]; ok {
			return " (" + truncLine(fmt.Sprint(v), 60) + ")"
		}
	}
	return ""
}
