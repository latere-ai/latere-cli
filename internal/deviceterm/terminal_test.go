// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package deviceterm

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"latere.ai/x/cella/device"
	v1 "latere.ai/x/cella/manifest/v1"
	"latere.ai/x/cella/worker"
)

// syncBuffer is an output a terminal writes while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls cond for five seconds, and fails naming what it waited for.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeEngine is an engine the test tells what happened: it publishes the
// events the test gives it to the terminal's subscription, and records the
// answers the terminal gives. It never closes its channel, so the terminal
// never takes a second subscription.
type fakeEngine struct {
	mu       sync.Mutex
	events   chan device.Event
	attached int
	status   device.Status
	answers  []string
}

func newFakeEngine(status device.Status) *fakeEngine {
	return &fakeEngine{events: make(chan device.Event, 64), status: status}
}

func (f *fakeEngine) Subscribe() (<-chan device.Event, func()) { return f.events, func() {} }

func (f *fakeEngine) Attach() func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached++
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.attached--
	}
}

func (f *fakeEngine) Status() device.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeEngine) Answer(id, answer, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, id+" "+answer+" "+by)
	return nil
}

func (f *fakeEngine) given() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.answers...)
}

func (f *fakeEngine) isAttached() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attached > 0
}

// terminalOn runs a terminal client of e that reads what the test types.
func terminalOn(t *testing.T, e Engine, interactive bool) (out *syncBuffer, typed io.Writer) {
	t.Helper()
	in, w := io.Pipe()
	out = &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Terminal{Engine: e, In: in, Out: out, Interactive: interactive, Home: "/h"}.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		<-done
	})
	return out, w
}

// typeLine types one line at the terminal.
func typeLine(t *testing.T, typed io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(typed, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalShowsTheEngine(t *testing.T) {
	e := newFakeEngine(device.Status{State: device.StateNotConnected})
	out, _ := terminalOn(t, e, false)
	// A serve that waits to be connected says so once, before any event.
	waitFor(t, "the waiting line", func() bool { return strings.Contains(out.String(), "Not connected.") })
	for _, ev := range []device.Event{
		{Kind: device.EventStatus, Status: &device.Status{State: device.StateNotConnected}},
		{Kind: device.EventStatus, Status: &device.Status{State: device.StateOnline}},
		{Kind: device.EventActivity, Activity: &device.Entry{Time: time.Now(), Type: "Exec", Session: "ses_x", Command: []string{"go", "test"}}},
		{Kind: device.EventActivity, Activity: &device.Entry{Time: time.Now(), Type: "Inspect", Session: "ses_x"}},
		{Kind: device.EventNotice, Notice: "the key is in a file"},
		{Kind: device.EventStarted, Approval: &device.Approval{Agent: "Site helper", Session: "ses_s", Folder: "/h/site", Write: true}},
		{Kind: device.EventStatus, Status: &device.Status{State: device.StateOnline, Sessions: 2}},
		{Kind: device.EventStatus, Status: &device.Status{State: device.StateOnline, Sessions: 2}},
		{Kind: device.EventStatus, Status: &device.Status{State: device.StateOffline, Detail: "the connection ended"}},
		{Kind: device.EventStatus, Status: &device.Status{State: device.StatePaused}},
	} {
		e.events <- ev
	}
	waitFor(t, "every line", func() bool { return strings.Contains(out.String(), "Paused.") })
	got := out.String()
	for _, want := range []string{
		"Online. No session is using this computer.", "ses_x  exec  go test", "Note: the key is in a file",
		"Site helper (ses_s, started by a person) is using ~/site (read-write).",
		"Online. 2 sessions are using this computer.", "Offline: the connection ended.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the terminal lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "inspect") {
		t.Errorf("the terminal printed a bookkeeping operation:\n%s", got)
	}
	for _, once := range []string{"Not connected.", "Online. 2 sessions"} {
		if n := strings.Count(got, once); n != 1 {
			t.Errorf("%q is printed %d times, want once:\n%s", once, n, got)
		}
	}
	if e.isAttached() {
		t.Error("a terminal no one can answer at attached to answer approvals")
	}
}

func TestTerminalAsksAndAnswers(t *testing.T) {
	e := newFakeEngine(device.Status{State: device.StateOnline})
	out, typed := terminalOn(t, e, true)
	waitFor(t, "the terminal to attach", e.isAttached)
	person := device.Approval{ID: "apr_1", Agent: "Site helper", AgentRef: "agt_1", AgentVersion: "4", Session: "ses_p", Folder: "/h/project", Write: true}
	scheduled := device.Approval{ID: "apr_2", Agent: "Site helper", Session: "ses_s", Trigger: "trg_1", FiresOn: device.FiresOnSchedule, Rememberable: true}
	e.events <- device.Event{Kind: device.EventAsked, Approval: &person}
	e.events <- device.Event{Kind: device.EventAsked, Approval: &scheduled}
	waitFor(t, "the first prompt", func() bool { return strings.Contains(out.String(), "Allow this session?") })
	prompt := out.String()
	for _, want := range []string{
		"A session wants to use this computer.", "agent    Site helper (agt_1, version 4)", "session  ses_p",
		"started  by a person", "folder   ~/project (read-write)", "Allow this session? [y]es / [n]o",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "[r]emember") || strings.Contains(prompt, "ses_s") {
		t.Errorf("the first prompt offers remember, or shows the second create before the first is answered:\n%s", prompt)
	}

	// An answer that is none is met with the hint; remember is no answer
	// for a session a person started.
	typeLine(t, typed, "maybe")
	typeLine(t, typed, "r")
	waitFor(t, "the hint", func() bool { return strings.Count(out.String(), "Type y to allow or n to refuse.") == 2 })
	typeLine(t, typed, "y")
	waitFor(t, "the answer", func() bool { return len(e.given()) == 1 })
	if got := e.given()[0]; got != "apr_1 allow this terminal" {
		t.Errorf("the terminal answered %q", got)
	}
	// The engine says the create was answered; the next one is asked.
	e.events <- device.Event{Kind: device.EventAnswered, Approval: &person, Answer: device.AnswerAllow, By: terminalClient}
	waitFor(t, "the second prompt", func() bool { return strings.Contains(out.String(), "[r]emember this trigger for 30 days") })
	if !strings.Contains(out.String(), "started  by trigger trg_1 (on a schedule)") {
		t.Errorf("the second prompt does not say the trigger:\n%s", out.String())
	}
	typeLine(t, typed, "r")
	waitFor(t, "the remember", func() bool { return len(e.given()) == 2 })
	if got := e.given()[1]; got != "apr_2 remember this terminal" {
		t.Errorf("the terminal answered %q", got)
	}
	e.events <- device.Event{Kind: device.EventAnswered, Approval: &scheduled, Answer: device.AnswerRemember, By: terminalClient}

	// Another client's answer and a withdrawal move the terminal on, each
	// said in its own words.
	other := device.Approval{ID: "apr_3", Session: "ses_o"}
	gone := device.Approval{ID: "apr_4", Session: "ses_w"}
	e.events <- device.Event{Kind: device.EventAsked, Approval: &other}
	e.events <- device.Event{Kind: device.EventAsked, Approval: &gone}
	e.events <- device.Event{Kind: device.EventAnswered, Approval: &other, Answer: device.AnswerDeny, By: "Latere for Mac"}
	waitFor(t, "the other client's answer", func() bool { return strings.Contains(out.String(), "Refused in Latere for Mac.") })
	waitFor(t, "the next prompt", func() bool { return strings.Contains(out.String(), "session  ses_w") })
	e.events <- device.Event{Kind: device.EventWithdrawn, Approval: &gone}
	waitFor(t, "the withdrawal", func() bool { return strings.Contains(out.String(), "The session stopped waiting for an answer.") })
	// A line typed with nothing waiting answers nothing.
	typeLine(t, typed, "y")
	e.events <- device.Event{Kind: device.EventNotice, Notice: "after the line"}
	waitFor(t, "the notice after the line", func() bool { return strings.Contains(out.String(), "after the line") })
	if n := len(e.given()); n != 2 {
		t.Errorf("the terminal gave %d answers, want 2: %v", n, e.given())
	}
}

func TestTerminalNotInteractiveAsksNothing(t *testing.T) {
	e := newFakeEngine(device.Status{State: device.StateOnline})
	out, _ := terminalOn(t, e, false)
	e.events <- device.Event{Kind: device.EventAsked, Approval: &device.Approval{ID: "apr_1", Session: "ses_p"}}
	e.events <- device.Event{Kind: device.EventNotice, Notice: "done"}
	waitFor(t, "the notice", func() bool { return strings.Contains(out.String(), "Note: done") })
	if strings.Contains(out.String(), "Allow this session?") {
		t.Errorf("a terminal that is not interactive asked:\n%s", out.String())
	}
}

// fakeAccount is the engine's Account; nothing here serves, so no key is
// minted.
type fakeAccount struct{}

func (fakeAccount) MintKey(context.Context, string, time.Duration) (device.Key, error) {
	return device.Key{}, nil
}
func (fakeAccount) RevokeKey(context.Context, string, string) error { return nil }
func (fakeAccount) Exists(context.Context, string) (bool, error)    { return true, nil }

// attachedEngine is Cella's engine, telling the test once the terminal has
// attached: the terminal subscribes first, so from then on it hears every
// create.
type attachedEngine struct {
	*device.Engine
	once     sync.Once
	attached chan struct{}
}

func (a *attachedEngine) Attach() func() {
	detach := a.Engine.Attach()
	a.once.Do(func() { close(a.attached) })
	return detach
}

// TestTerminalAnswersTheEngine is the terminal over Cella's engine itself: a
// create on a connected computer is asked at the terminal, and the answer
// typed there is the engine's verdict.
func TestTerminalAnswersTheEngine(t *testing.T) {
	home := t.TempDir()
	paths := device.Paths{Home: home, Config: filepath.Join(home, ".config", "latere"), State: filepath.Join(home, ".state"), Cache: filepath.Join(home, ".cache")}
	project := filepath.Join(home, "project")
	if err := device.SavePolicy(paths.PolicyFile(), device.Policy{
		Device: "dev-terminaltest0000", Name: "Test Mac", SessionStart: device.SessionStartAsk,
		Folders: []device.Folder{{Path: project, Write: true}}, Network: device.Network{Mode: device.NetworkNone},
	}); err != nil {
		t.Fatal(err)
	}
	labels := device.SessionLabels{Session: "example.com/session", Agent: "example.com/agent", AgentName: "example.com/agent-name"}
	e := &attachedEngine{
		Engine:   device.New(device.Config{Paths: paths, Keys: device.OpenKeys(paths.KeyFile()), Account: fakeAccount{}, Labels: labels}),
		attached: make(chan struct{}),
	}
	out, typed := terminalOn(t, e, true)
	// A create before the terminal attached would find no one to ask, and
	// be refused before any prompt.
	select {
	case <-e.attached:
	case <-time.After(10 * time.Second):
		t.Fatal("the terminal never attached")
	}
	create := func(session string) worker.Create {
		return worker.Create{
			Labels: map[string]string{labels.Session: session, labels.Agent: "agt_1"},
			Sandbox: v1.Sandbox{
				Metadata: v1.Metadata{Annotations: map[string]string{labels.AgentName: "Site helper"}},
				Spec:     v1.SandboxSpec{Workspace: v1.Workspace{Host: project}},
				Status:   v1.SandboxStatus{ID: "sbx_" + session},
			},
		}
	}
	for i, tc := range []struct {
		answer string
		allow  bool
	}{{"y", true}, {"n", false}} {
		session := fmt.Sprintf("ses_%d", i)
		verdict := make(chan worker.Verdict, 1)
		go func() { verdict <- e.Admit(t.Context(), create(session)) }()
		waitFor(t, "the prompt for "+session, func() bool { return strings.Contains(out.String(), "session  "+session) })
		typeLine(t, typed, tc.answer)
		select {
		case v := <-verdict:
			if v.Allow != tc.allow || (!v.Allow && v.Reason != device.ReasonRefused) {
				t.Errorf("%s answered %q reads %+v", session, tc.answer, v)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no verdict after %q; the terminal shows:\n%s", tc.answer, out.String())
		}
	}
	if !strings.Contains(out.String(), "agent    Site helper (agt_1)") {
		t.Errorf("the prompt does not name the agent by the embedder's labels:\n%s", out.String())
	}
}

func TestPromptWording(t *testing.T) {
	for _, tc := range []struct {
		a    device.Approval
		want string
	}{
		{device.Approval{}, "agent    an unnamed agent"},
		{device.Approval{Agent: "agt_1", AgentRef: "agt_1", AgentVersion: "2"}, "agent    agt_1 (version 2)"},
		{device.Approval{Agent: "Helper", AgentRef: "agt_1"}, "agent    Helper (agt_1)"},
		{device.Approval{}, "session  not named"},
		{device.Approval{}, "folder   none; the session works in its own scratch directory"},
		{device.Approval{Folder: "/h/notes"}, "folder   ~/notes (read-only)"},
	} {
		if got := Prompt(tc.a, "/h"); !strings.Contains(got, tc.want) {
			t.Errorf("Prompt(%+v) lacks %q:\n%s", tc.a, tc.want, got)
		}
	}
	for answer, want := range map[string]string{
		device.AnswerAllow: "Allowed in x.", device.AnswerRemember: "Allowed and remembered for 30 days in x.",
		device.AnswerDeny: "Refused in x.", answerNoOne: "Refused: no one was left to answer.", "other": "Answered in x.",
	} {
		if got := answeredElsewhere(answer, "x"); got != want {
			t.Errorf("answeredElsewhere(%s) = %q", answer, got)
		}
	}
	if statusLine(device.Status{State: device.StateOnline, Sessions: 1}) != "Online. 1 session is using this computer." ||
		statusLine(device.Status{State: device.StateOffline}) != "Offline. Reconnecting." || statusLine(device.Status{}) != "Connecting." {
		t.Error("a status line is misworded")
	}
	if ParseAnswer(" YES ", false) != device.AnswerAllow || ParseAnswer("no", true) != device.AnswerDeny || ParseAnswer("remember", true) != device.AnswerRemember ||
		ParseAnswer("r", false) != "" || ParseAnswer("maybe", true) != "" {
		t.Error("an answer is misread")
	}
	if !strings.Contains(answerHint(true), "r to remember") {
		t.Error("the hint for a rememberable create does not name r")
	}
}

func TestStartedLine(t *testing.T) {
	for _, tc := range []struct {
		a    device.Approval
		want string
	}{
		{device.Approval{Agent: "Site helper", Session: "ses_1", Folder: "/h/code/site", Write: true},
			"Site helper (ses_1, started by a person) is using ~/code/site (read-write)."},
		{device.Approval{Agent: "Site helper", Session: "ses_2", Trigger: "trg_1", FiresOn: device.FiresOnSchedule, Folder: "/h/notes"},
			"Site helper (ses_2, started by trigger trg_1 on a schedule) is using ~/notes (read-only)."},
		{device.Approval{Agent: "Site helper", Session: "ses_3", Trigger: "trg_2", FiresOn: device.FiresOnEvents, Folder: "/h/notes"},
			"Site helper (ses_3, started by trigger trg_2 on delivered events) is using ~/notes (read-only)."},
		{device.Approval{Trigger: "trg_3"},
			"An unnamed agent (a session not named, started by trigger trg_3) is using its own scratch directory."},
	} {
		if got := StartedLine(tc.a, "/h"); got != tc.want {
			t.Errorf("StartedLine = %q, want %q", got, tc.want)
		}
	}
}
