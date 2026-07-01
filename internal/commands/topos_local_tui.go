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
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
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
// The transcript is a list of typed blocks (user / assistant / tool / result /
// notice) rendered to strings and joined with blank lines for breathing room.
// Assistant text is rendered as Markdown via glamour once a turn settles (raw
// while streaming, so it stays fast). A run streams asynchronously: runner.Turn
// executes in a goroutine and the Observer forwards each event via prog.Send;
// nothing writes to stdout/stderr (a stray write corrupts the alt-screen), so SDK
// logs go to io.Discard for the session.

// collapseThreshold is the tool-result line count above which output is folded to
// a summary (expand with Ctrl+O).
const collapseThreshold = 6

// localEventMsg carries one run event into the Bubble Tea update loop.
type localEventMsg struct{ ev topos.Event }

// localTurnDoneMsg reports a completed (or failed) turn, its transcript, and how
// long it took (for the "✻ …for Ns" footer).
type localTurnDoneMsg struct {
	transcript []models.Message
	err        error
	elapsed    time.Duration
}

type blockKind int

const (
	blkUser blockKind = iota
	blkAssistant
	blkTool
	blkResult
	blkNotice
)

// block is one entry in the transcript. text holds the raw content (Markdown for
// assistant blocks); rendered/cached memoize the styled output so re-rendering the
// transcript each frame (for the ticking status) stays cheap.
type block struct {
	kind      blockKind
	text      string // raw content (Markdown for assistant)
	name      string // tool name (blkTool)
	args      string // tool arg summary (blkTool)
	isErr     bool   // error result (blkResult) or error notice
	streaming bool   // assistant block still receiving deltas → render raw
	collapsed bool   // tool result folded to a summary
	rendered  string
	cached    bool
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

	vp        viewport.Model
	input     textinput.Model
	spin      spinner.Model
	glam      *glamour.TermRenderer
	glamWidth int

	blocks     []block
	transcript []models.Message // conversation threaded to the runner
	running    bool
	turnStart  time.Time
	turnVerb   string
	turnTip    string
	turnCount  int
	inTok      int
	outTok     int
	expand     bool // Ctrl+O: show tool output in full
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
	sp.Spinner = spinner.Spinner{Frames: []string{"✻", "✽", "✳", "✶", "✷", "✸"}, FPS: time.Second / 8}
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
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		return m.onKey(msg)
	case localEventMsg:
		m.appendEvent(msg.ev)
		return m, nil
	case localTurnDoneMsg:
		m.running = false
		m.closeStreaming()
		if msg.err != nil {
			m.appendNotice(styleErr.Render("error: "+msg.err.Error()), true)
		} else {
			m.transcript = msg.transcript
			m.appendNotice(styleDim.Render(m.footer(msg.elapsed)), false)
		}
		m.refresh()
		return m, nil
	case spinner.TickMsg:
		if !m.running {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.refresh() // update the live elapsed/token line
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
	case tea.KeyCtrlO:
		// Toggle tool-output folding for the whole transcript.
		m.expand = !m.expand
		for i := range m.blocks {
			if m.blocks[i].kind == blkResult {
				m.blocks[i].collapsed = !m.expand
				m.blocks[i].cached = false
			}
		}
		m.refresh()
		return m, nil
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyUp, tea.KeyDown:
		// Scroll the transcript (the input is single-line, so ↑/↓ are free).
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
	m.appendUser(prompt)
	m.running = true
	m.turnStart = time.Now()
	m.turnVerb = workVerbs[m.turnCount%len(workVerbs)]
	m.turnTip = workTips[m.turnCount%len(workTips)]
	m.turnCount++
	m.refresh()
	tr := m.transcript
	go func() {
		start := time.Now()
		res, err := m.runner.Turn(m.ctx, topos.TurnInput{
			Sandbox:           m.sb,
			SandboxID:         "local",
			SystemPrompt:      localSystemPrompt,
			Tools:             m.builtins,
			InitialTranscript: tr,
			UserPrompt:        prompt,
		})
		m.prog.Send(localTurnDoneMsg{transcript: res.Transcript, err: err, elapsed: time.Since(start)})
	}()
	return m, m.spin.Tick
}

func (m *localTUI) onSlash(line string) (tea.Model, tea.Cmd) {
	cmd := strings.Fields(line)[0]
	switch cmd {
	case "/quit", "/exit":
		return m, tea.Quit
	case "/help":
		m.appendNotice(styleDim.Render(localTUIHelp()), false)
	case "/model":
		m.switchModel(strings.TrimSpace(strings.TrimPrefix(line, cmd)))
	default:
		m.appendNotice(styleDim.Render("unknown command "+cmd+" — /help"), false)
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
			m.appendNotice(styleDim.Render("could not list Lux models; use /model <name>"), false)
			return
		}
		m.appendNotice(styleDim.Render("Models (use /model <name>):\n  "+strings.Join(list, "\n  ")), false)
		return
	}
	b, err := buildLocalModel(m.ctx, name)
	if err != nil {
		m.appendNotice(styleErr.Render("switch failed: "+err.Error()), true)
		return
	}
	if err := m.rebuild(b); err != nil {
		m.appendNotice(styleErr.Render(err.Error()), true)
		return
	}
	m.appendNotice(styleDim.Render("switched to "+m.curModel), false)
}

// appendEvent folds one run event into the transcript blocks.
func (m *localTUI) appendEvent(e topos.Event) {
	switch e.Name {
	case topos.EventTextDelta:
		var p textDeltaPayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil || p.Text == "" {
			return
		}
		if a := m.openAssistant(); a != nil {
			a.text += p.Text
		} else {
			m.blocks = append(m.blocks, block{kind: blkAssistant, text: p.Text, streaming: true})
		}
	case "PreToolUse":
		var p preToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil {
			return
		}
		m.closeStreaming()
		m.blocks = append(m.blocks, block{kind: blkTool, name: p.ToolCall.Name, args: summarizeToolInput(p.ToolCall.Input)})
	case topos.EventPostToolUse:
		var p postToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) != nil {
			return
		}
		m.blocks = append(m.blocks, block{kind: blkResult, text: p.Result.Content, isErr: p.Result.IsError, collapsed: !m.expand})
	case topos.EventUsage:
		var p usagePayload
		if json.Unmarshal(e.PayloadJSON, &p) == nil {
			m.inTok, m.outTok = p.Total.InputTokens, p.Total.OutputTokens
		}
	default:
		return
	}
	m.refresh()
}

// openAssistant returns the current streaming assistant block, or nil.
func (m *localTUI) openAssistant() *block {
	if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blkAssistant && m.blocks[n-1].streaming {
		return &m.blocks[n-1]
	}
	return nil
}

// closeStreaming settles the open assistant block so it renders as Markdown.
func (m *localTUI) closeStreaming() {
	if a := m.openAssistant(); a != nil {
		a.streaming = false
		a.cached = false
	}
}

func (m *localTUI) appendUser(text string) {
	m.closeStreaming()
	m.blocks = append(m.blocks, block{kind: blkUser, text: text})
}

func (m *localTUI) appendNotice(styled string, isErr bool) {
	m.closeStreaming()
	m.blocks = append(m.blocks, block{kind: blkNotice, text: styled, isErr: isErr})
}

// footer is the settled "✻ <verb> for Ns" line shown after a turn completes.
func (m *localTUI) footer(d time.Duration) string {
	verb := m.turnVerb
	if verb == "" {
		verb = "Worked"
	}
	s := fmt.Sprintf("✻ %s for %ds", verb, int(d.Seconds()))
	if m.outTok > 0 {
		s += fmt.Sprintf(" · ↓ %d tokens", m.outTok)
	}
	return s
}

// workingLine is the live status shown while a turn runs.
func (m *localTUI) workingLine() string {
	secs := int(time.Since(m.turnStart).Seconds())
	aux := fmt.Sprintf(" (%ds", secs)
	if m.outTok > 0 {
		aux += fmt.Sprintf(" · ↓ %d tokens", m.outTok)
	}
	aux += ")"
	head := styleVerb.Render(m.spin.View()+" "+m.turnVerb+"…") + styleDim.Render(aux)
	return head + "\n" + styleDim.Render("  ⎿ "+m.turnTip)
}

// content renders the whole transcript (plus the live working line) for the
// viewport, memoizing per-block so the ticking status stays cheap.
func (m *localTUI) content() string {
	parts := make([]string, 0, len(m.blocks)+1)
	for i := range m.blocks {
		parts = append(parts, m.renderBlock(i))
	}
	if m.running {
		parts = append(parts, m.workingLine())
	}
	return strings.Join(parts, "\n\n")
}

func (m *localTUI) renderBlock(i int) string {
	b := &m.blocks[i]
	// A streaming assistant block changes every delta; render it raw and skip the
	// cache. Everything else is memoized until invalidated.
	if b.kind == blkAssistant && b.streaming {
		return styleAsstDot.Render("⏺") + " " + m.wrap(b.text, m.vp.Width-2)
	}
	if b.cached {
		return b.rendered
	}
	var s string
	switch b.kind {
	case blkUser:
		s = styleUserMsg.Width(m.vp.Width).Render("❯ " + b.text)
	case blkAssistant:
		s = styleAsstDot.Render("⏺") + " " + m.markdown(b.text)
	case blkTool:
		s = styleToolDot.Render("⏺") + " " + styleToolName.Render(b.name) + styleDim.Render(b.args)
	case blkResult:
		s = m.renderResult(b)
	case blkNotice:
		s = m.wrap(b.text, m.vp.Width)
	}
	b.rendered, b.cached = s, true
	return s
}

// renderResult renders a tool result, folded to a summary when collapsed.
func (m *localTUI) renderResult(b *block) string {
	lines := strings.Split(strings.TrimRight(b.text, "\n"), "\n")
	style := styleDim
	if b.isErr {
		style = styleErr
	}
	branch := styleDim.Render("  ⎿ ")
	if b.collapsed && len(lines) > collapseThreshold {
		return branch + style.Render(truncLine(lines[0], m.vp.Width-24)) +
			styleDim.Render(fmt.Sprintf("  +%d lines · Ctrl+O", len(lines)-1))
	}
	var out strings.Builder
	for i, ln := range lines {
		prefix := branch
		if i > 0 {
			prefix = "    "
		}
		out.WriteString(prefix + style.Render(truncLine(ln, m.vp.Width-6)))
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// markdown renders assistant Markdown via glamour, falling back to raw text.
func (m *localTUI) markdown(s string) string {
	if m.glam == nil {
		return m.wrap(s, m.vp.Width-2)
	}
	out, err := m.glam.Render(s)
	if err != nil {
		return m.wrap(s, m.vp.Width-2)
	}
	return strings.Trim(out, "\n")
}

// wrap soft-wraps plain/styled text to width (the viewport horizontal-scrolls
// otherwise).
func (m *localTUI) wrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

// refresh syncs the viewport to the transcript, pinning to the bottom only when
// the user was already there (so scrolling back isn't yanked).
func (m *localTUI) refresh() {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(m.content())
	if atBottom {
		m.vp.GotoBottom()
	}
}

// layout sizes the header/viewport/input/status to the terminal and rebuilds the
// width-dependent Markdown renderer.
func (m *localTUI) layout() {
	// headerH=3 (logo + info, three rows), inputH=3 (rounded border top + content
	// + bottom), statusH=1 — must match the rows View actually renders.
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
	m.setupGlamour(m.vp.Width - 2)
	m.refresh()
}

// markdownStyle is the dark glamour style with the document margin zeroed, so
// assistant text aligns flush under the ⏺ marker instead of being indented. An
// explicit style (vs auto) also renders Markdown deterministically regardless of
// terminal color detection.
func markdownStyle() ansi.StyleConfig {
	s := styles.DarkStyleConfig
	zero := uint(0)
	s.Document.Margin = &zero
	return s
}

// setupGlamour (re)builds the Markdown renderer for the current width and
// invalidates cached blocks so they re-wrap.
func (m *localTUI) setupGlamour(w int) {
	if w <= 0 || w == m.glamWidth {
		return
	}
	if r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle()), glamour.WithWordWrap(w)); err == nil {
		m.glam, m.glamWidth = r, w
	}
	for i := range m.blocks {
		m.blocks[i].cached = false
	}
}

func (m *localTUI) View() string {
	if !m.ready {
		return "Starting Topos…"
	}
	inputBox := styleInputBorder.Width(m.width - 2).Render(m.input.View())
	return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), m.vp.View(), inputBox, m.statusView())
}

// toposLogo is the three-line brand mark shown at the left of the header (a
// blocky "T"). Kept as a const so the art is easy to swap.
const toposLogo = "█████\n  █\n  █"

func (m *localTUI) headerView() string {
	logo := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true).Render(toposLogo)
	info := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Render("Topos")+styleDim.Render("  local · v"+m.version),
		styleDim.Render(m.curModel),
		styleDim.Render(homeAbbrev(m.cwd)),
	)
	// Three lines (logo and info both 3 rows), no trailing newline — layout()
	// budgets headerH=3.
	return lipgloss.JoinHorizontal(lipgloss.Top, logo, "   ", info)
}

func (m *localTUI) statusView() string {
	// The model is already in the header; the status line carries token usage
	// (and a working spinner) on the left, key hints on the right.
	left := fmt.Sprintf("%d↑ %d↓ tok", m.inTok, m.outTok)
	if m.running {
		left = m.spin.View() + " working  " + left
	}
	right := "↑↓ scroll · Ctrl+O expand · /model · Ctrl+D quit"
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return styleDim.Render(left) + strings.Repeat(" ", gap) + styleDim.Render(right)
}

// homeAbbrev replaces the user's home directory prefix with ~.
func homeAbbrev(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if p == h {
			return "~"
		}
		if strings.HasPrefix(p, h+string(os.PathSeparator)) {
			return "~" + p[len(h):]
		}
	}
	return p
}

func localTUIHelp() string {
	return strings.TrimSpace(`
Commands:
  /model [name]   switch model (name switches directly; none lists your Lux models)
  /help           show this help
  /quit, /exit    leave (or Ctrl+D)
Ctrl+O expands/folds tool output · ↑↓ or PgUp/PgDn scroll.`)
}

// workVerbs and workTips add a little personality to the working line, varied by
// turn so it doesn't read the same every time.
var (
	workVerbs = []string{"Churning", "Whirlpooling", "Percolating", "Simmering", "Noodling", "Cogitating", "Tinkering"}
	workTips  = []string{
		"Tip: Ctrl+O expands tool output.",
		"Tip: /model switches models mid-session.",
		"Tip: ↑/↓ or PgUp/PgDn scroll the transcript.",
		"Tip: run with -p \"…\" for a one-shot, scriptable answer.",
	}
)

// runLocalTUI launches the full-screen local TUI.
func runLocalTUI(ctx context.Context, version, cwd string, sb sandbox.Provider, builtins *tools.Registry, brain models.Model) error {
	// Alt-screen: a stray stdout/stderr write corrupts the display, so silence
	// the SDK's logger for the session (failures still surface in the transcript).
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m, err := newLocalTUI(ctx, version, cwd, sb, builtins, brain)
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	m.prog = p
	_, err = p.Run()
	return err
}

// Styles specific to the local TUI (block styles are shared from topos_local.go).
var (
	styleUserMsg     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252")).Padding(0, 1)
	styleVerb        = lipgloss.NewStyle().Foreground(lipgloss.Color("173"))
	styleInputBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
)
