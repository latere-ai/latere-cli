// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"latere.ai/x/topos"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models"
	"latere.ai/x/topos/sandbox"
)

// The full-screen TUI for `latere topos --local`, in the spirit of Claude Code /
// Codex: an alt-screen app with a header, a scrolling transcript that streams, a
// bordered input, and a status line. It reuses the verified core (buildLocalModel,
// host sandbox, tools, model switching) unchanged — this is presentation only.
//
// A run streams asynchronously: runner.Turn executes in a goroutine, and the
// Observer forwards each event to the program via prog.Send (localEventMsg); the
// Update loop folds it into the transcript buffer. Nothing here writes to
// stdout/stderr directly — in alt-screen a stray write corrupts the display, so
// SDK logs are routed to io.Discard for the session.

// localEventMsg carries one run event into the Bubble Tea update loop.
type localEventMsg struct{ ev topos.Event }

// localTurnDoneMsg reports a completed (or failed) turn and the new transcript.
type localTurnDoneMsg struct {
	transcript []models.Message
	err        error
}

type localTUI struct {
	ctx     context.Context
	prog    *tea.Program // set right after NewProgram; used by the Observer
	version string
	cwd     string

	sb       sandbox.Provider
	builtins *tools.Registry
	runner   *topos.Runner
	curModel string

	vp    viewport.Model
	input textinput.Model
	spin  spinner.Model

	transcript []models.Message // conversation threaded to the runner
	buf        strings.Builder  // rendered transcript shown in the viewport
	inText     bool             // an assistant text block is streaming
	running    bool
	inTok      int
	outTok     int
	ready      bool // a WindowSizeMsg has sized the layout
	width      int
	height     int
}

func newLocalTUI(ctx context.Context, version, cwd string, sb sandbox.Provider, builtins *tools.Registry, brain models.Model) (*localTUI, error) {
	in := textinput.New()
	in.Placeholder = "Ask anything, or /help"
	in.Prompt = "❯ "
	in.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m := &localTUI{ctx: ctx, version: version, cwd: cwd, sb: sb, builtins: builtins, input: in, spin: sp}
	if err := m.rebuild(brain); err != nil {
		return nil, err
	}
	return m, nil
}

// rebuild points the runner at a new model, keeping the same async Observer.
func (m *localTUI) rebuild(b models.Model) error {
	obs := func(e topos.Event) {
		if m.prog != nil {
			m.prog.Send(localEventMsg{ev: e})
		}
	}
	rn, err := topos.NewRunner(topos.Options{Brain: b, Sandbox: m.sb, Observer: obs})
	if err != nil {
		return err
	}
	m.runner, m.curModel = rn, modelString(b)
	return nil
}

func (m *localTUI) Init() tea.Cmd { return textinput.Blink }

func (m *localTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	case localEventMsg:
		m.appendEvent(msg.ev)
		return m, nil
	case localTurnDoneMsg:
		m.running = false
		if msg.err != nil {
			m.appendLine(styleErr.Render("error: " + msg.err.Error()))
		} else {
			m.transcript = msg.transcript
		}
		m.refresh()
		return m, nil
	case spinner.TickMsg:
		if !m.running {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *localTUI) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyCtrlD:
		return m, tea.Quit
	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case tea.KeyEnter:
		if m.running {
			return m, nil
		}
		line := strings.TrimSpace(m.input.Value())
		if line == "" {
			return m, nil
		}
		m.input.Reset()
		if strings.HasPrefix(line, "/") {
			return m.onSlash(line)
		}
		return m.startTurn(line)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// startTurn echoes the prompt and runs the turn asynchronously; the Observer
// streams events back while it runs.
func (m *localTUI) startTurn(prompt string) (tea.Model, tea.Cmd) {
	m.appendLine("\n" + styleUser.Render("❯ "+prompt))
	m.inText, m.running = false, true
	tr := m.transcript
	go func() {
		res, err := m.runner.Turn(m.ctx, topos.TurnInput{
			Sandbox:           m.sb,
			SandboxID:         "local",
			SystemPrompt:      localSystemPrompt,
			Tools:             m.builtins,
			InitialTranscript: tr,
			UserPrompt:        prompt,
		})
		m.prog.Send(localTurnDoneMsg{transcript: res.Transcript, err: err})
	}()
	return m, m.spin.Tick
}

func (m *localTUI) onSlash(line string) (tea.Model, tea.Cmd) {
	cmd := strings.Fields(line)[0]
	switch cmd {
	case "/quit", "/exit":
		return m, tea.Quit
	case "/help":
		m.appendLine(localTUIHelp())
	case "/model":
		m.switchModel(strings.TrimSpace(strings.TrimPrefix(line, cmd)))
	default:
		m.appendLine(styleDim.Render("unknown command " + cmd + " — /help"))
	}
	return m, nil
}

// switchModel changes the model in place. With a name it switches directly; with
// none it lists the Anthropic models Lux exposes (an overlay picker is a
// follow-up — /model <name> is the reliable path in the full-screen app).
func (m *localTUI) switchModel(name string) {
	if name == "" {
		list, err := fetchLuxModels(m.ctx)
		if err != nil || len(list) == 0 {
			m.appendLine(styleDim.Render("could not list Lux models; use /model <name>"))
			return
		}
		m.appendLine(styleDim.Render("Models (use /model <name>):\n  " + strings.Join(list, "\n  ")))
		return
	}
	b, err := buildLocalModel(m.ctx, name)
	if err != nil {
		m.appendLine(styleErr.Render("switch failed: " + err.Error()))
		return
	}
	if err := m.rebuild(b); err != nil {
		m.appendLine(styleErr.Render(err.Error()))
		return
	}
	m.appendLine(styleDim.Render("switched to " + m.curModel))
}

// appendEvent renders one run event into the transcript buffer (no printing).
func (m *localTUI) appendEvent(e topos.Event) {
	switch e.Name {
	case topos.EventTextDelta:
		var p textDeltaPayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil || p.Text == "" {
			return
		}
		if !m.inText {
			m.buf.WriteString("\n" + styleAsstDot.Render("⏺") + " ")
			m.inText = true
		}
		m.buf.WriteString(p.Text)
	case "PreToolUse":
		var p preToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil {
			return
		}
		if m.inText {
			m.buf.WriteByte('\n')
			m.inText = false
		}
		m.buf.WriteString("\n" + styleToolDot.Render("⏺") + " " +
			styleToolName.Render(p.ToolCall.Name) + styleDim.Render(summarizeToolInput(p.ToolCall.Input)) + "\n")
	case topos.EventPostToolUse:
		var p postToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil {
			return
		}
		body := truncLine(p.Result.Content, 200)
		if p.Result.IsError {
			body = styleErr.Render(body)
		} else {
			body = styleDim.Render(body)
		}
		m.buf.WriteString(styleDim.Render("  ⎿ ") + body + "\n")
	case topos.EventUsage:
		var p usagePayload
		if json.Unmarshal(e.PayloadJSON, &p) == nil {
			m.inTok, m.outTok = p.Total.InputTokens, p.Total.OutputTokens
		}
		return
	default:
		return
	}
	m.refresh()
}

// appendLine adds a standalone line to the transcript (help, errors, notices).
func (m *localTUI) appendLine(s string) {
	m.inText = false
	m.buf.WriteString(s + "\n")
	m.refresh()
}

// refresh syncs the viewport to the buffer, keeping the view pinned to the
// bottom only when the user was already there (so scrolling back isn't yanked).
func (m *localTUI) refresh() {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(m.buf.String())
	if atBottom {
		m.vp.GotoBottom()
	}
}

// layout sizes the header/viewport/input/status to the terminal.
func (m *localTUI) layout() {
	const headerH, inputH, statusH = 3, 3, 1
	vpH := m.height - headerH - inputH - statusH
	if vpH < 3 {
		vpH = 3
	}
	if !m.ready {
		m.vp = viewport.New(m.width, vpH)
		m.ready = true
	} else {
		m.vp.Width, m.vp.Height = m.width, vpH
	}
	m.input.Width = m.width - 6
	m.refresh()
}

func (m *localTUI) View() string {
	if !m.ready {
		return "Starting Topos…"
	}
	inputBox := styleInputBorder.Width(m.width - 2).Render(m.input.View())
	return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), m.vp.View(), inputBox, m.statusView())
}

func (m *localTUI) headerView() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("◤ Topos")
	return title + " " + styleDim.Render("local · v"+m.version) + "\n" +
		styleDim.Render(m.curModel+"  ·  "+m.cwd) + "\n"
}

func (m *localTUI) statusView() string {
	left := fmt.Sprintf("%s · %d↑ %d↓ tok", m.curModel, m.inTok, m.outTok)
	if m.running {
		left = m.spin.View() + " working…  " + left
	}
	right := "/help · /model · Ctrl+D quit"
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return styleDim.Render(left) + strings.Repeat(" ", gap) + styleDim.Render(right)
}

func localTUIHelp() string {
	return strings.TrimSpace(`
Commands:
  /model [name]   switch model (name switches directly; none lists your Lux models)
  /help           show this help
  /quit, /exit    leave (or Ctrl+D)
PgUp/PgDn scroll the transcript.`)
}

// runLocalTUI launches the full-screen local TUI.
func runLocalTUI(ctx context.Context, version, cwd string, sb sandbox.Provider, builtins *tools.Registry, brain models.Model) error {
	// Alt-screen: a stray stdout/stderr write corrupts the display, so silence
	// the SDK's logger for the session (failures still surface in the transcript).
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m, err := newLocalTUI(ctx, version, cwd, sb, builtins, brain)
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	m.prog = p
	_, err = p.Run()
	return err
}

// Styles specific to the local TUI (block styles are shared from topos_local.go).
var (
	styleUser        = lipgloss.NewStyle().Bold(true)
	styleInputBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
)
