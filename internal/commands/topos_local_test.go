// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"latere.ai/x/pkg/llmdialect/ir"
	"latere.ai/x/pkg/llmdialect/lux"
	"latere.ai/x/topos/harness"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models"
	"latere.ai/x/topos/models/scripted"
	"latere.ai/x/topos/session"
)

func TestBuildLocalModelCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Isolate the on-disk config so a real login can't leak in.
	t.Setenv("LATERE_AGENT_PROVIDER_FILE", filepath.Join(t.TempDir(), "provider.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth.json"))
	if _, err := buildLocalModel(context.Background(), ""); err == nil {
		t.Fatal("expected an error with no credentials")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	m, err := buildLocalModel(context.Background(), "")
	if err != nil || m.name != defaultAnthropicModel {
		t.Fatalf("buildLocalModel with API key = (%q, %v), want %s", m.name, err, defaultAnthropicModel)
	}
	_, conn, entry, err := m.connect(t.Context())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if conn.BaseURL != anthropicURL || conn.Credential != "sk-test" || conn.Model != defaultAnthropicModel || entry.InputWindow == 0 {
		t.Fatalf("the direct connection is %+v with figures %+v", conn, entry)
	}
	// A model the catalog does not know is refused at the turn, not
	// sent with no figures.
	unknown, err := buildLocalModel(t.Context(), "claude-unknown-0")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := unknown.connect(t.Context()); err == nil {
		t.Fatal("an unknown model connected")
	}
}

// scriptedModel is a local model that plays script, one step per request,
// in place of a provider.
func scriptedModel(t *testing.T, script string) localModel {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.yaml")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	player := &scripted.Model{}
	return localModel{name: "scripted", connect: func(context.Context) (models.Model, models.Connection, models.Entry, error) {
		return player, models.Connection{BaseURL: "scripted:" + path, Model: "scripted", Family: models.FamilyOther},
			models.Entry{Name: "scripted", Family: models.FamilyOther, InputWindow: 200_000, MaxOutputTokens: 32_000}, nil
	}}
}

// recorder keeps the events a local agent emits.
type recorder struct {
	mu     sync.Mutex
	events []localEvent
}

func (r *recorder) emit(e localEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) all() []localEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]localEvent(nil), r.events...)
}

// waitFor returns the first event match holds for, waiting for one to
// arrive.
func (r *recorder) waitFor(t *testing.T, match func(localEvent) bool) localEvent {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range r.all() {
			if match(e) {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no event matched; the agent told %+v", r.all())
	return localEvent{}
}

// kinds returns the events of kind, in the order the agent told them.
func (r *recorder) kinds(kind localEventKind) []localEvent {
	var out []localEvent
	for _, e := range r.all() {
		if e.kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// startAgent starts a local agent in a new directory and closes it when
// the test ends.
func startAgent(t *testing.T, store session.Store, model localModel, emit func(localEvent)) *localAgent {
	t.Helper()
	agent, err := newLocalAgentOn(t.Context(), store, t.TempDir(), model, emit)
	if err != nil {
		t.Fatalf("newLocalAgentOn: %v", err)
	}
	t.Cleanup(func() {
		if err := agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return agent
}

// turnAsync runs a turn of agent in the background; the channel answers
// its error.
func turnAsync(t *testing.T, agent *localAgent, prompt string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- agent.turn(t.Context(), prompt) }()
	return done
}

// waitTurn returns the error of a turn turnAsync started.
func waitTurn(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("the turn did not end")
		return nil
	}
}

// logOf is the session's whole log.
func logOf(t *testing.T, agent *localAgent) []session.Event {
	t.Helper()
	evs, err := agent.store.Events(t.Context(), agent.id, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// stateOf is the state the log gives the waiting message id, "" for a
// message that is not waiting.
func stateOf(evs []session.Event, id string) session.MessageState {
	for _, w := range session.WaitingMessages(evs) {
		if w.Event.ID == id {
			return w.State
		}
	}
	return ""
}

// gate is a file a scripted command waits for in the working directory.
const gateCommand = "for i in $(seq 1 600); do [ -f gate ] && break; sleep 0.05; done; echo gate-opened"

// openGate lets the command gateCommand runs in agent's directory end.
func openGate(t *testing.T, agent *localAgent) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(agent.workdir, "gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLocalMessageWhileACommandRunsIsReadAtTheNextStep sends a message
// while the turn's command runs: the next request of the same turn reads
// it, led by the line that says it was sent during the work, and the
// terminal is told it where the messages.read placed it, after the
// command's result.
func TestLocalMessageWhileACommandRunsIsReadAtTheNextStep(t *testing.T) {
	rec := &recorder{}
	agent := startAgent(t, session.NewMemoryStore(), scriptedModel(t, `steps:
  - tool_calls:
      - name: bash
        input: {command: "`+gateCommand+`"}
  - expect: {text_contains: "sent this while you were working:"}
    text: "Using port 9000."
`), rec.emit)
	done := turnAsync(t, agent, "start the server")
	rec.waitFor(t, func(e localEvent) bool { return e.kind == localToolUse })
	id, start, err := agent.send(t.Context(), "use port 9000", false)
	if err != nil || start {
		t.Fatalf("send while the turn runs = (%v, %v), want the running drive to read it", start, err)
	}
	openGate(t, agent)
	if err := waitTurn(t, done); err != nil {
		t.Fatalf("turn: %v", err)
	}
	evs := logOf(t, agent)
	if got := stateOf(evs, id); got != session.MessageRead {
		t.Fatalf("the message is %q, want read", got)
	}
	var order []localEventKind
	for _, e := range rec.all() {
		if e.kind == localToolResult || e.kind == localRead {
			order = append(order, e.kind)
		}
	}
	if !slices.Equal(order, []localEventKind{localToolResult, localRead}) {
		t.Errorf("the terminal was told %v, want the result and then the message", order)
	}
	reads := rec.kinds(localRead)
	if len(reads) != 1 || reads[0].id != id || reads[0].text != "use port 9000" || reads[0].cut {
		t.Errorf("reads = %+v, want the message read in its turn", reads)
	}
	for _, e := range evs {
		var m session.UserMessage
		if e.Type == session.TypeUserMessage && e.Decode(&m) == nil && m.SendNow {
			t.Errorf("%s carries send_now; neither message asked for it", e.ID)
		}
	}
}

// TestLocalSendNowMovesACommand sends a message to be read now while a
// command runs: the host moves the command to the background, the call
// returns within the grace, and the next request of the same turn reads
// the message. Nothing was cut, so the turn ends as any other.
func TestLocalSendNowMovesACommand(t *testing.T) {
	rec := &recorder{}
	agent := startAgent(t, session.NewMemoryStore(), scriptedModel(t, `steps:
  - tool_calls:
      - name: bash
        input: {command: "`+gateCommand+`"}
  - expect: {tool_result_contains: "moved to the background", text_contains: "sent this while you were working:"}
    text: "Read it."
`), rec.emit)
	t.Cleanup(func() { openGate(t, agent) })
	done := turnAsync(t, agent, "build it")
	rec.waitFor(t, func(e localEvent) bool { return e.kind == localToolUse })
	id, _, err := agent.send(t.Context(), "stop, use the other target", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitTurn(t, done); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := stateOf(logOf(t, agent), id); got != session.MessageRead {
		t.Fatalf("the message is %q, want read", got)
	}
	res := rec.kinds(localToolResult)
	if len(res) != 1 || !strings.Contains(res[0].text, "sent to be read now") {
		t.Errorf("results = %+v, want the command moved by the message", res)
	}
	if reads := rec.kinds(localRead); len(reads) != 1 || reads[0].cut {
		t.Errorf("reads = %+v, want the message read in its own turn", reads)
	}
}

// holdTool is a tool whose call runs until it is canceled, so a message
// sent to be read now cuts it once the grace passes.
type holdTool struct{}

func (holdTool) Definition() tools.Definition {
	return tools.Definition{Name: "hold", Description: "Waits until canceled.", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (holdTool) Properties() tools.Properties { return tools.Properties{Effect: tools.EffectNone} }

func (holdTool) Run(ctx context.Context, _ tools.Call) (tools.Result, error) {
	<-ctx.Done()
	return tools.Result{}, ctx.Err()
}

// TestLocalSendNowCutIsNotAnError sends a message to be read now while a
// call that cannot move runs: the call is cut after the grace and the turn
// stops interrupted by send_now, which turn does not report as an error;
// it follows the next turn, which reads the message led by the line that
// says the person stopped the work.
func TestLocalSendNowCutIsNotAnError(t *testing.T) {
	rec := &recorder{}
	model := scriptedModel(t, `steps:
  - tool_calls:
      - name: hold
        input: {}
  - expect: {text_contains: "stopped your work to send this:"}
    text: "Stopped."
`)
	agent := startAgent(t, session.NewMemoryStore(), model, rec.emit)
	if err := agent.tools.AddBuiltin(holdTool{}); err != nil {
		t.Fatal(err)
	}
	done := turnAsync(t, agent, "wait")
	rec.waitFor(t, func(e localEvent) bool { return e.kind == localToolUse && e.name == "hold" })
	id, _, err := agent.send(t.Context(), "do this instead", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitTurn(t, done); err != nil {
		t.Fatalf("a turn stopped by send now reported %v", err)
	}
	evs := logOf(t, agent)
	cut := slices.ContainsFunc(evs, func(e session.Event) bool {
		var st session.SessionStatus
		return e.Type == session.TypeSessionStatus && e.Decode(&st) == nil &&
			st.StopReason == session.StopInterrupted && st.Detail == session.DetailSendNow
	})
	if !cut {
		t.Fatal("no turn stopped interrupted by send_now")
	}
	if got := stateOf(evs, id); got != session.MessageRead {
		t.Fatalf("the message is %q, want read", got)
	}
	reads := rec.kinds(localRead)
	if len(reads) != 1 || reads[0].id != id || !reads[0].cut {
		t.Errorf("reads = %+v, want the message read after the stop it made", reads)
	}
	if text := rec.kinds(localText); len(text) == 0 || text[len(text)-1].text != "Stopped." {
		t.Errorf("text = %+v, want the next turn's answer", text)
	}
}

// TestLocalSendNowWithdrawnInTheGraceIsNotAnError takes back a message
// sent to be read now while its cut waits out the grace: the cut still
// stops the turn, interrupted by send_now, and nothing is left to read, so
// Drive returns that stop. It is the person's choice, not an error.
func TestLocalSendNowWithdrawnInTheGraceIsNotAnError(t *testing.T) {
	rec := &recorder{}
	agent := startAgent(t, session.NewMemoryStore(), scriptedModel(t, `steps:
  - tool_calls:
      - name: hold
        input: {}
`), rec.emit)
	if err := agent.tools.AddBuiltin(holdTool{}); err != nil {
		t.Fatal(err)
	}
	done := turnAsync(t, agent, "wait")
	rec.waitFor(t, func(e localEvent) bool { return e.kind == localToolUse && e.name == "hold" })
	id, _, err := agent.send(t.Context(), "never mind", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.withdraw(t.Context(), id); err != nil {
		t.Fatalf("withdraw in the grace: %v", err)
	}
	if err := waitTurn(t, done); err != nil {
		t.Fatalf("a turn stopped by send now reported %v", err)
	}
	evs := logOf(t, agent)
	var last session.SessionStatus
	for _, e := range evs {
		var st session.SessionStatus
		if e.Type == session.TypeSessionStatus && e.Decode(&st) == nil {
			last = st
		}
	}
	if last.StopReason != session.StopInterrupted || last.Detail != session.DetailSendNow {
		t.Fatalf("the session last stopped %s %s, want interrupted send_now", last.StopReason, last.Detail)
	}
	if reads := rec.kinds(localRead); len(reads) != 0 {
		t.Errorf("reads = %+v, want none: the message was taken back", reads)
	}
}

func TestStopErrorOfASendNowStop(t *testing.T) {
	for _, tc := range []struct {
		out  harness.Outcome
		fail bool
	}{
		{harness.Outcome{StopReason: session.StopEndTurn}, false},
		{harness.Outcome{StopReason: session.StopCompleted}, false},
		{harness.Outcome{StopReason: session.StopInterrupted, Detail: session.DetailSendNow}, false},
		{harness.Outcome{StopReason: session.StopInterrupted}, true},
		{harness.Outcome{StopReason: session.StopError, Detail: "model_error"}, true},
	} {
		if err := stopError(tc.out); (err != nil) != tc.fail {
			t.Errorf("stopError(%s %s) = %v, want an error: %v", tc.out.StopReason, tc.out.Detail, err, tc.fail)
		}
	}
}

// lateStore hands out leases whose release first appends a person's
// message, once: a message sent after the runner last looked for input,
// before the drive that runs ends.
type lateStore struct {
	session.Store
	once sync.Once
	late func() session.Event
}

func (s *lateStore) Acquire(ctx context.Context, id string, h session.Holder) (session.Lease, error) {
	l, err := s.Store.Acquire(ctx, id, h)
	if err != nil {
		return nil, err
	}
	return &lateLease{Lease: l, s: s, id: id}, nil
}

type lateLease struct {
	session.Lease
	s  *lateStore
	id string
}

func (l *lateLease) Release() error {
	var err error
	l.s.once.Do(func() {
		var evs []session.Event
		if evs, err = l.s.Events(context.Background(), l.id, 1, 0); err != nil {
			return
		}
		batch := []session.Event{l.s.late()}
		last := evs[len(evs)-1].Seq
		session.Stamp(l.id, last, batch)
		_, err = l.s.Append(context.Background(), l.id, last, batch)
	})
	return errors.Join(err, l.Lease.Release())
}

// TestLocalDriveReadsAMessageAsATurnEnds appends a message after the
// runner's last look for input and before its drive returns: the drive
// that ran sees it and runs the turn it starts.
func TestLocalDriveReadsAMessageAsATurnEnds(t *testing.T) {
	rec := &recorder{}
	store := &lateStore{Store: session.NewMemoryStore()}
	model := scriptedModel(t, `steps:
  - text: "one"
  - expect: {text_contains: "second"}
    text: "two"
`)
	agent := startAgent(t, store, model, rec.emit)
	var late session.Event
	store.late = func() session.Event {
		e, err := session.NewEvent(session.TypeUserMessage, session.UserMessage{
			Sender: agent.person, Content: []lux.Block{{Type: ir.BlockText, Text: "second"}},
		}, time.Now())
		if err != nil {
			t.Error(err)
		}
		late = e
		return e
	}
	if err := agent.turn(t.Context(), "first"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	var text strings.Builder
	for _, e := range rec.kinds(localText) {
		text.WriteString(e.text)
	}
	if text.String() != "onetwo" {
		t.Fatalf("text = %q, want both turns' answers", text.String())
	}
	prompts := rec.kinds(localPrompt)
	if len(prompts) != 2 || prompts[1].id != late.ID || prompts[1].text != "second" {
		t.Errorf("prompts = %+v, want the late message as the second turn's", prompts)
	}
	agent.sendMu.Lock()
	driving := agent.driving
	agent.sendMu.Unlock()
	if driving {
		t.Error("the drive ended and the agent still says it drives")
	}
}

// TestLocalWithdraw takes back a waiting message: no request reads it,
// and a second withdrawal is refused. A message the agent read, one that
// started its turn, and one another person sent are refused too.
func TestLocalWithdraw(t *testing.T) {
	rec := &recorder{}
	agent := startAgent(t, session.NewMemoryStore(), scriptedModel(t, `steps:
  - tool_calls:
      - name: bash
        input: {command: "`+gateCommand+`"}
  - expect: {text_contains: "kept"}
    text: "ok"
`), rec.emit)
	done := turnAsync(t, agent, "start")
	rec.waitFor(t, func(e localEvent) bool { return e.kind == localToolUse })
	taken, _, err := agent.send(t.Context(), "taken back", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.withdraw(t.Context(), taken); err != nil {
		t.Fatalf("withdraw a waiting message: %v", err)
	}
	if err := agent.withdraw(t.Context(), taken); !errors.Is(err, errNotWaiting) {
		t.Fatalf("a second withdrawal = %v, want errNotWaiting", err)
	}
	kept, _, err := agent.send(t.Context(), "kept", false)
	if err != nil {
		t.Fatal(err)
	}
	openGate(t, agent)
	if err := waitTurn(t, done); err != nil {
		t.Fatalf("turn: %v", err)
	}
	evs := logOf(t, agent)
	if got := stateOf(evs, taken); got != session.MessageWithdrawn {
		t.Errorf("the withdrawn message is %q", got)
	}
	for _, e := range evs {
		var p session.MessagesRead
		if e.Type == session.TypeMessagesRead && e.Decode(&p) == nil && slices.Contains(p.EventIDs, taken) {
			t.Errorf("messages.read %v names the withdrawn message", p.EventIDs)
		}
	}
	if reads := rec.kinds(localRead); len(reads) != 1 || reads[0].id != kept {
		t.Errorf("reads = %+v, want only the kept message", reads)
	}
	if err := agent.withdraw(t.Context(), kept); !errors.Is(err, errAlreadyRead) {
		t.Errorf("withdraw a read message = %v, want errAlreadyRead", err)
	}
	first := rec.kinds(localPrompt)[0].id
	if err := agent.withdraw(t.Context(), first); !errors.Is(err, errNotWaiting) {
		t.Errorf("withdraw the message that started its turn = %v, want errNotWaiting", err)
	}
}

// A message another person sent is theirs to take back.
func TestWithdrawableRefusesAnotherSender(t *testing.T) {
	running, err := session.NewEvent(session.TypeSessionStatus, session.SessionStatus{Status: session.StatusRunning}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	msg, err := session.NewEvent(session.TypeUserMessage, session.UserMessage{
		Sender: session.Sender{Subject: "local:other", Kind: session.SenderPerson}, Content: []lux.Block{{Type: ir.BlockText, Text: "hi"}},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	evs := []session.Event{running, msg}
	session.Stamp("ses_1", 0, evs)
	if err := withdrawable(evs, msg.ID, "local:me"); !errors.Is(err, errNotWaiting) || !strings.Contains(err.Error(), "local:other") {
		t.Fatalf("withdrawable = %v, want a refusal that names the sender", err)
	}
	if err := withdrawable(evs, msg.ID, "local:other"); err != nil {
		t.Fatalf("withdrawable by its sender = %v", err)
	}
}

// TestLocalLoopEndToEnd drives the whole local stack with a scripted model:
// the model runs a command and writes a file, the host machine really runs
// both in the working directory, neither asks for a confirmation, and the
// terminal is told the streamed text, each call and its result, and the
// tokens. Only the model is played from a script.
func TestLocalLoopEndToEnd(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{}
	model := scriptedModel(t, `steps:
  - tool_calls:
      - name: bash
        input: {command: "echo marker-12345"}
      - name: write
        input: {path: note.txt, content: "hi"}
    usage: {input_tokens: 10, output_tokens: 4}
  - text: "Done."
    usage: {input_tokens: 20, output_tokens: 2}
`)
	agent, err := newLocalAgent(t.Context(), dir, model, rec.emit)
	if err != nil {
		t.Fatalf("newLocalAgent: %v", err)
	}
	data := agent.dataDir
	if err := agent.turn(t.Context(), "run it"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := agent.close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "note.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("note.txt = %q, %v; want the file the model wrote", got, err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("the agent's data directory outlived it: %v", err)
	}
	var calls, text, results strings.Builder
	var usage localEvent
	for _, e := range rec.all() {
		switch e.kind {
		case localText:
			text.WriteString(e.text)
		case localToolUse:
			calls.WriteString(e.name + " ")
		case localToolResult:
			if e.isErr {
				t.Errorf("a call failed: %s", e.text)
			}
			results.WriteString(e.text + "\n")
		case localUsage:
			usage = e
		}
	}
	if calls.String() != "bash write " {
		t.Errorf("calls = %q, want bash then write", calls.String())
	}
	if !strings.Contains(results.String(), "marker-12345") {
		t.Errorf("results %q lack the command's output (the host ran it)", results.String())
	}
	if text.String() != "Done." {
		t.Errorf("streamed text = %q, want Done.", text.String())
	}
	if usage.in != 30 || usage.out != 6 {
		t.Errorf("usage = %d in, %d out; want the session's 30 and 6", usage.in, usage.out)
	}
}

// A turn that ends any other way than its end is the turn's error, so the
// terminal shows it.
func TestLocalTurnReportsAStoppedTurn(t *testing.T) {
	agent, err := newLocalAgent(t.Context(), t.TempDir(), scriptedModel(t, "steps:\n  - fail: {status: 400}\n"), func(localEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := agent.turn(t.Context(), "hello"); err == nil {
		t.Fatal("a turn whose model failed reported success")
	}
}

// flakyStore fails what the test sets it to: conflicts conflicting appends
// before the next one goes through, broken refuses every append, and
// blind refuses every watch.
type flakyStore struct {
	session.Store
	mu        sync.Mutex
	conflicts int
	broken    bool
	blind     bool
}

func (s *flakyStore) set(f func(*flakyStore)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *flakyStore) Append(ctx context.Context, id string, after uint64, evs []session.Event) (uint64, error) {
	s.mu.Lock()
	broken, conflict := s.broken, s.conflicts > 0
	if conflict {
		s.conflicts--
	}
	s.mu.Unlock()
	switch {
	case broken:
		return 0, errors.New("the store is down")
	case conflict:
		return 0, session.ErrSequenceConflict
	}
	return s.Store.Append(ctx, id, after, evs)
}

func (s *flakyStore) Watch(ctx context.Context, id string, from uint64) (<-chan session.Event, error) {
	s.mu.Lock()
	blind := s.blind
	s.mu.Unlock()
	if blind {
		return nil, errors.New("the store cannot be watched")
	}
	return s.Store.Watch(ctx, id, from)
}

// TestLocalAgentStoreFailures: a send tries again after the runner
// appended first, and gives up after localAppendTries; a store that fails
// fails the send, and a drive that cannot follow the log ends and lets the
// next message start one.
func TestLocalAgentStoreFailures(t *testing.T) {
	store := &flakyStore{Store: session.NewMemoryStore()}
	agent := startAgent(t, store, scriptedModel(t, "steps: []\n"), func(localEvent) {})

	store.set(func(s *flakyStore) { s.conflicts = localAppendTries - 1 })
	if _, start, err := agent.send(t.Context(), "after a conflict", false); err != nil || !start {
		t.Fatalf("send after conflicts = (%v, %v), want it appended", start, err)
	}
	agent.driving = false
	store.set(func(s *flakyStore) { s.conflicts = localAppendTries })
	if _, _, err := agent.send(t.Context(), "never", false); !errors.Is(err, session.ErrSequenceConflict) {
		t.Fatalf("send through conflicts only = %v, want the conflict", err)
	}
	store.set(func(s *flakyStore) { s.conflicts, s.broken = 0, true })
	if err := agent.turn(t.Context(), "down"); err == nil || agent.driving {
		t.Fatalf("turn on a broken store = %v, driving %v; want the store's error", err, agent.driving)
	}
	store.set(func(s *flakyStore) { s.broken, s.blind = false, true })
	if err := agent.turn(t.Context(), "blind"); err == nil {
		t.Fatal("a drive that could not follow the log reported success")
	}
	agent.sendMu.Lock()
	driving := agent.driving
	agent.sendMu.Unlock()
	if driving {
		t.Fatal("a drive that failed left the agent driving, so no message would start the next")
	}
}

// An event the terminal cannot read is skipped, not shown half-read.
func TestLocalRecordSkipsAnEventItCannotRead(t *testing.T) {
	rec := &recorder{}
	agent := &localAgent{emit: rec.emit}
	for _, typ := range []session.Type{session.TypeAgentToolUse, session.TypeToolResult, session.TypeUserMessage, session.TypeModelRequest} {
		agent.record(session.Event{Type: typ, Seq: agent.seen + 1, Payload: json.RawMessage(`{`)})
	}
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("the agent told %+v of events it could not read", got)
	}
	if agent.seen != 4 {
		t.Errorf("seen = %d, want every event counted", agent.seen)
	}
}

// TestLocalSystemPromptNamesNoTool keeps tool names out of the system
// prompt: each tool's description already says what it does, and a name in
// prose becomes a dangling reference the moment the tool set changes.
func TestLocalSystemPromptNamesNoTool(t *testing.T) {
	for _, tool := range tools.Builtins() {
		if name := tool.Definition().Name; strings.Contains(localSystemPrompt, name) {
			t.Errorf("localSystemPrompt names tool %q; describe the behavior instead", name)
		}
	}
}
