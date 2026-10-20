// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

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
	"sync"
	"time"

	"latere.ai/x/pkg/llmdialect/ir"
	"latere.ai/x/pkg/llmdialect/lux"
	"latere.ai/x/topos/harness"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/machine"
	"latere.ai/x/topos/machine/host"
	"latere.ai/x/topos/prompts"
	"latere.ai/x/topos/runner"
	"latere.ai/x/topos/session"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// isInteractiveTTY reports whether both stdin and stdout are terminals, so the
// full-screen TUI can take over the screen (piped/redirected runs use the REPL).
func isInteractiveTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// quietSDKLogs silences the runtime's INFO chatter that otherwise
// interleaves with the chat. The runtime logs through slog.Default;
// warnings and errors are kept. Genuine failures still surface as returned
// errors.
func quietSDKLogs() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
}

// localSystemPrompt is the default instruction for `latere agents run`: a
// coding assistant working directly in the user's directory.
const localSystemPrompt = `You are a coding assistant running on the user's local machine, in their working directory.
The tools operate on their real files. Edit files with the file tools rather than through shell commands, and use the shell to run and verify. Be concise.`

// runToposLocal runs the agent loop entirely on this machine, like Claude Code:
// the model is the chosen provider, the workspace is your directory, and
// tools execute directly on your files. No control plane and no Cella.
// With oneShot set it runs a single prompt and exits; otherwise it is a REPL.
func runToposLocal(ctx context.Context, dir, modelName, oneShot, version string) error {
	quietSDKLogs()
	brain, err := buildLocalModel(ctx, modelName)
	if errors.Is(err, errNeedAuth) {
		// No credential yet. In an interactive terminal, show the provider picker
		// and retry; otherwise (one-shot -p, or piped) fail with a clear message
		// rather than launching a TUI with no TTY.
		if oneShot != "" || !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("no model credential: run `latere agents provider`, or set ANTHROPIC_API_KEY")
		}
		if perr := runAuthPicker(ctx); perr != nil {
			return perr
		}
		brain, err = buildLocalModel(ctx, modelName)
	}
	if err != nil {
		return err
	}

	// Interactive on a real terminal → the full-screen TUI. One-shot (-p) and
	// piped/non-TTY input fall through to the scriptable line REPL below.
	if oneShot == "" && isInteractiveTTY() {
		return runLocalTUI(ctx, version, dir, brain)
	}

	render := &localRenderer{}
	agent, err := newLocalAgent(ctx, dir, brain, render.render)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := agent.close(context.WithoutCancel(ctx)); cerr != nil {
			fmt.Fprintln(os.Stderr, styleErr.Render("error: "+cerr.Error()))
		}
	}()
	turn := func(prompt string) error {
		err := agent.turn(ctx, prompt)
		render.endTurn()
		return err
	}

	if oneShot != "" {
		return turn(oneShot)
	}

	fmt.Printf("Latere agent (local) in %s\n", agent.workdir)
	fmt.Printf("model: %s   ·   /model to switch, /help for commands, Ctrl+D to quit\n", agent.modelName())
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
			if quit := handleLocalCommand(ctx, line, agent); quit {
				return nil
			}
			continue
		}
		if err := turn(line); err != nil {
			fmt.Fprintln(os.Stderr, styleErr.Render("error: "+err.Error()))
		}
	}
}

// localAgentID is the agent a local session runs: no stored agent, the
// runtime's built-in one with the instructions below.
const localAgentID = "agent_00000000000000000000000000"

// localAgent is one session of the Latere agent on this machine: its log,
// kept in memory for the length of the command, the runner that drives
// it, the host machine its tools act on, and the model its turns run on.
type localAgent struct {
	store   session.Store
	runner  *runner.Runner
	id      string
	workdir string
	// dataDir holds what the machine keeps outside the working directory,
	// the output a tool spills, and is removed when the agent closes.
	dataDir string
	machine *host.Host
	tools   *tools.Registry
	person  session.Sender
	emit    func(localEvent)

	mu    sync.Mutex
	model localModel
	// in and out are the tokens the session's requests read and wrote.
	in, out int64
}

// newLocalAgent starts a session in dir on model, telling emit what each
// turn does.
func newLocalAgent(ctx context.Context, dir string, model localModel, emit func(localEvent)) (*localAgent, error) {
	workdir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	dataDir, err := os.MkdirTemp("", "latere-agent-")
	if err != nil {
		return nil, err
	}
	a := &localAgent{store: session.NewMemoryStore(), workdir: workdir, dataDir: dataDir, emit: emit, model: model}
	if err = a.start(ctx); err != nil {
		return nil, errors.Join(err, a.close(context.WithoutCancel(ctx)))
	}
	return a, nil
}

// start opens the host machine, registers the built-in tools and creates
// the session.
func (a *localAgent) start(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	user := os.Getenv("USER")
	if user == "" {
		user = "local"
	}
	a.person = session.Sender{Subject: "local:" + user, Name: user, Kind: session.SenderPerson}
	s := session.New(session.AgentRef{ID: localAgentID, Name: "latere", Version: 1}, a.person, session.RunnerExternal,
		session.Machine{Kind: machine.KindHost, Workdir: a.workdir}, time.Now())
	s.Writer = &session.Writer{Kind: session.RunnerExternal, Subject: a.person.Subject, Since: s.CreatedAt}
	if a.machine, err = host.Open(host.Options{
		Workdir: a.workdir, SpillDir: filepath.Join(a.dataDir, "spill"), Home: home, DataDir: a.dataDir, ID: s.ID,
	}); err != nil {
		return err
	}
	a.tools = tools.NewRegistry()
	for _, t := range tools.Builtins() {
		if err = a.tools.AddBuiltin(t); err != nil {
			return err
		}
	}
	if err = a.store.Create(ctx, s, nil); err != nil {
		return err
	}
	a.id = s.ID
	a.runner, err = runner.New(runner.Options{
		Store: a.store, Harness: a.config, ID: fmt.Sprintf("latere-%d", os.Getpid()), Kind: runner.KindLocal,
	})
	return err
}

// close stops the machine and removes what it kept outside the working
// directory.
func (a *localAgent) close(ctx context.Context) error {
	var err error
	if a.machine != nil {
		err = a.machine.Stop(ctx)
	}
	return errors.Join(err, os.RemoveAll(a.dataDir))
}

// modelName is the model the next turn runs on.
func (a *localAgent) modelName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model.name
}

// setModel runs the next turn on m.
func (a *localAgent) setModel(m localModel) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = m
}

// config is the session's harness: the turn's model, the host machine in
// the working directory with the built-in tools, and a policy under which
// every call runs without asking, as a coding assistant of the person's
// own does. The runtime takes no checkpoint, so nothing is written into the
// person's repository but what the agent itself writes.
func (a *localAgent) config(ctx context.Context, _ session.Session) (harness.Config, error) {
	a.mu.Lock()
	m := a.model
	a.mu.Unlock()
	model, conn, entry, err := m.connect(ctx)
	if err != nil {
		return harness.Config{}, err
	}
	return harness.Config{
		Model: model, Connection: conn, Entry: entry,
		Machine: a.machine, Tools: a.tools,
		Policy:       harness.Policy{Mode: harness.ModeConfirm, AlwaysAllow: a.tools.Names()},
		Instructions: localSystemPrompt,
		Prompt:       prompts.HarnessOptions{Host: true},
		Observer:     localObserver{emit: a.emit},
		Checkpoint: func(context.Context, int, string) (*session.CheckpointRef, error) {
			return nil, nil
		},
	}, nil
}

// turn sends prompt and runs the turn it starts to its end, telling emit
// each call, its result and the tokens used as the log records them.
func (a *localAgent) turn(ctx context.Context, prompt string) error {
	start, err := a.store.Get(ctx, a.id)
	if err != nil {
		return err
	}
	msg, err := session.NewEvent(session.TypeUserMessage, session.UserMessage{
		Sender: a.person, Content: []lux.Block{{Type: ir.BlockText, Text: prompt}},
	}, time.Now())
	if err != nil {
		return err
	}
	evs := []session.Event{msg}
	session.Stamp(a.id, start.LastSeq, evs)
	if _, err = a.store.Append(ctx, a.id, start.LastSeq, evs); err != nil {
		return err
	}
	watching, stop := context.WithCancel(ctx)
	defer stop()
	watch, err := a.store.Watch(watching, a.id, start.LastSeq+1)
	if err != nil {
		return err
	}
	var (
		wg   sync.WaitGroup
		last uint64
	)
	wg.Go(func() {
		for e := range watch {
			last = e.Seq
			a.record(e)
		}
	})
	out, derr := a.runner.Drive(ctx, a.id)
	stop()
	wg.Wait()
	rest, err := a.store.Events(context.WithoutCancel(ctx), a.id, max(last, start.LastSeq)+1, 0)
	if err != nil {
		return errors.Join(derr, err)
	}
	for _, e := range rest {
		a.record(e)
	}
	if derr != nil {
		return derr
	}
	switch out.StopReason {
	case session.StopEndTurn, session.StopCompleted:
		return nil
	}
	return fmt.Errorf("the turn stopped: %s %s", out.StopReason, out.Detail)
}

// record tells emit what one event of the log shows the person: a call of
// the session's own thread, its result, and a request's tokens.
func (a *localAgent) record(e session.Event) {
	switch e.Type {
	case session.TypeAgentToolUse:
		var u session.AgentToolUse
		if e.Decode(&u) != nil || e.Thread != "" {
			return
		}
		a.emit(localEvent{kind: localToolUse, name: u.Name, input: u.Input})
	case session.TypeToolResult:
		var r session.ToolResult
		if e.Decode(&r) != nil || e.Thread != "" {
			return
		}
		var text []string
		for _, b := range r.Content {
			if b.Type == ir.BlockText {
				text = append(text, b.Text)
			}
		}
		a.emit(localEvent{kind: localToolResult, text: strings.Join(text, "\n"), isErr: r.IsError})
	case session.TypeModelRequest:
		var mr session.ModelRequest
		if e.Decode(&mr) != nil || mr.Usage == nil {
			return
		}
		a.mu.Lock()
		a.in += mr.Usage.InputTokens
		a.out += mr.Usage.OutputTokens
		in, out := a.in, a.out
		a.mu.Unlock()
		a.emit(localEvent{kind: localUsage, in: in, out: out})
	}
}

// localObserver tells emit the text of the session's own thread as the
// model streams it.
type localObserver struct{ emit func(localEvent) }

func (o localObserver) OnDelta(d harness.Delta) {
	if d.Thread == "" && d.Event.Type == ir.EventTextDelta && d.Event.Delta != "" {
		o.emit(localEvent{kind: localText, text: d.Event.Delta})
	}
}

func (localObserver) OnReset(string, int, int) {}

// localEventKind is what one localEvent is.
type localEventKind int

const (
	localText localEventKind = iota
	localToolUse
	localToolResult
	localUsage
)

// localEvent is one thing the terminal shows of a turn: text as the model
// streams it, a tool call with its input, a call's result, or the tokens the
// session used so far. The line REPL and the full-screen TUI read the same
// events.
type localEvent struct {
	kind    localEventKind
	text    string
	name    string
	input   json.RawMessage
	isErr   bool
	in, out int64
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
// text, tool calls, and results are cleanly separated. The streamed text and
// the log's calls arrive on different goroutines, so it renders one event at a
// time.
type localRenderer struct {
	mu     sync.Mutex
	inText bool // an assistant text block is currently streaming
}

func (r *localRenderer) render(e localEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e.kind {
	case localText:
		if !r.inText {
			fmt.Print("\n" + styleAsstDot.Render("⏺") + " ")
			r.inText = true
		}
		fmt.Print(e.text)
	case localToolUse:
		r.closeText()
		fmt.Printf("\n%s %s%s\n", styleToolDot.Render("⏺"),
			styleToolName.Render(e.name), styleDim.Render(summarizeToolInput(e.input)))
	case localToolResult:
		line := truncLine(e.text, 120)
		if e.isErr {
			fmt.Printf("%s%s\n", styleDim.Render("  ⎿ "), styleErr.Render(line))
			return
		}
		fmt.Printf("%s%s\n", styleDim.Render("  ⎿ "), styleDim.Render(line))
	}
}

// closeText ends an open assistant text block with a newline; its caller
// holds mu.
func (r *localRenderer) closeText() {
	if r.inText {
		fmt.Println()
		r.inText = false
	}
}

// endTurn terminates the current turn's output before the next prompt.
func (r *localRenderer) endTurn() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeText()
}

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

// truncLine collapses content to a single trimmed line capped at n runes.
func truncLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
