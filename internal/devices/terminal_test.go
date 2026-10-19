// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package devices

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// terminalOn runs a terminal client of e that reads what the test types.
func terminalOn(t *testing.T, e *Engine, home string, interactive bool) (out *syncBuffer, typed io.Writer) {
	t.Helper()
	in, w := io.Pipe()
	out = &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Terminal{Engine: e, In: in, Out: out, Interactive: interactive, Home: home}.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = w.Close()
		<-done
	})
	if interactive {
		waitFor(t, "the terminal to attach", func() bool { return e.broker.present() })
	}
	return out, w
}

// promptEngine is an engine that decides creates without serving: the host
// policy names testEnv and grants ~/project read-write.
func promptEngine(t *testing.T, paths Paths, now func() time.Time) *Engine {
	t.Helper()
	e := New(Config{Paths: paths, Keys: OpenKeys(paths.KeyFile()), Account: &fakeAccount{}, Now: now})
	e.policy = Policy{Device: testEnv}
	e.host = v1.HostInfo{Grants: []v1.HostGrant{{Path: filepath.Join(paths.Home, "project"), Write: true}}}
	return e
}

// creating is a create for a session with labels, named Site helper.
func creating(home string, labels map[string]string) worker.Create {
	return worker.Create{Labels: labels, Sandbox: v1.Sandbox{
		Metadata: v1.Metadata{Labels: labels, Annotations: map[string]string{AnnotationAgent: "Site helper"}},
		Spec:     v1.SandboxSpec{Workspace: v1.Workspace{Host: filepath.Join(home, "project")}},
		Status:   v1.SandboxStatus{ID: "sbx_t"},
	}}
}

// asking runs one create through the engine, waits for the prompt, types
// each line, and answers the verdict and the prompt it saw.
func asking(t *testing.T, e *Engine, out *syncBuffer, typed io.Writer, c worker.Create, lines ...string) (worker.Verdict, string) {
	t.Helper()
	before := strings.Count(out.String(), "A session wants to use this computer.")
	verdict := make(chan worker.Verdict, 1)
	go func() { verdict <- e.Admit(context.Background(), c) }()
	if len(lines) == 0 {
		select {
		case v := <-verdict:
			return v, ""
		case <-time.After(5 * time.Second):
			t.Fatal("a create that should not ask is waiting")
		}
	}
	waitFor(t, "the prompt", func() bool { return strings.Count(out.String(), "A session wants to use this computer.") > before })
	all := out.String()
	prompt := all[strings.LastIndex(all, "A session wants to use this computer."):]
	for _, line := range lines {
		if _, err := io.WriteString(typed, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case v := <-verdict:
		return v, prompt
	case <-time.After(10 * time.Second):
		t.Fatalf("no verdict after typing %q; the terminal shows:\n%s", lines, out.String())
	}
	return worker.Verdict{}, ""
}

func TestApprovePrompt(t *testing.T) {
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	clock := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	e := promptEngine(t, paths, func() time.Time { return clock })
	out, typed := terminalOn(t, e, paths.Home, true)
	person := map[string]string{LabelSession: "ses_p", LabelAgent: "agt_1", LabelVersion: "4"}
	scheduled := map[string]string{LabelSession: "ses_s", LabelAgent: "agt_1", LabelVersion: "4", LabelTrigger: "trg_1", LabelFiresOn: FiresOnSchedule}
	events := map[string]string{LabelSession: "ses_e", LabelAgent: "agt_1", LabelVersion: "4", LabelTrigger: "trg_2", LabelFiresOn: FiresOnEvents}
	remembered := func() []Remembered {
		p, _, err := LoadPolicy(paths.PolicyFile())
		if err != nil {
			t.Fatal(err)
		}
		return p.RememberedTriggers
	}

	t.Run("y admits once", func(t *testing.T) {
		v, prompt := asking(t, e, out, typed, creating(paths.Home, person), "y")
		if !v.Allow {
			t.Fatalf("y did not admit: %+v", v)
		}
		for _, want := range []string{
			"agent    Site helper (agt_1, version 4)", "session  ses_p", "started  by a person",
			"folder   ~/project (read-write)", "Allow this session? [y]es / [n]o",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("the prompt lacks %q:\n%s", want, prompt)
			}
		}
		if strings.Contains(prompt, "[r]emember") {
			t.Errorf("a session a person started offers remember:\n%s", prompt)
		}
		// Admitted once: the same session's next create asks again.
		if v, _ := asking(t, e, out, typed, creating(paths.Home, person), "y"); !v.Allow {
			t.Errorf("the second create read %+v", v)
		}
	})

	t.Run("n refuses with the person's words", func(t *testing.T) {
		v, _ := asking(t, e, out, typed, creating(paths.Home, person), "n")
		if v.Allow || v.Reason != ReasonRefused {
			t.Errorf("n reads %+v", v)
		}
	})

	t.Run("a session a person started is never remembered", func(t *testing.T) {
		v, _ := asking(t, e, out, typed, creating(paths.Home, person), "r", "y")
		if !v.Allow {
			t.Fatalf("the create read %+v", v)
		}
		if !strings.Contains(out.String(), "Type y to allow or n to refuse.") {
			t.Errorf("r for a person's session was not refused with the hint:\n%s", out.String())
		}
		if len(remembered()) != 0 {
			t.Errorf("a person's session was remembered: %+v", remembered())
		}
	})

	t.Run("r is offered for a scheduled trigger and remembers it", func(t *testing.T) {
		v, prompt := asking(t, e, out, typed, creating(paths.Home, scheduled), "r")
		if !v.Allow {
			t.Fatalf("r did not admit: %+v", v)
		}
		for _, want := range []string{"started  by trigger trg_1 (on a schedule)", "[r]emember this trigger for 30 days",
			"Remembering lets this trigger run here with no one asked"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("the prompt lacks %q:\n%s", want, prompt)
			}
		}
		got := remembered()
		if len(got) != 1 || got[0].Trigger != "trg_1" || got[0].AgentVersion != "4" || !got[0].Until.Equal(clock.Add(RememberFor)) {
			t.Errorf("the remembered entry is %+v", got)
		}
	})

	t.Run("a remembered trigger runs without asking until its agent changes", func(t *testing.T) {
		before := strings.Count(out.String(), "started by trigger trg_1 on a schedule) is using")
		if v, _ := asking(t, e, out, typed, creating(paths.Home, scheduled)); !v.Allow {
			t.Errorf("a remembered trigger was not admitted: %+v", v)
		}
		// Admitted with no one asked, it is still told to the person.
		waitFor(t, "the line for the remembered trigger", func() bool {
			return strings.Count(out.String(), "started by trigger trg_1 on a schedule) is using") > before
		})
		changed := map[string]string{}
		maps.Copy(changed, scheduled)
		changed[LabelVersion] = "5"
		if v, prompt := asking(t, e, out, typed, creating(paths.Home, changed), "n"); v.Allow || !strings.Contains(prompt, "version 5") {
			t.Errorf("a changed agent read %+v without its prompt", v)
		}
	})

	t.Run("a remembered trigger asks again once it expires", func(t *testing.T) {
		later := promptEngine(t, paths, func() time.Time { return clock.Add(RememberFor + time.Minute) })
		laterOut, laterTyped := terminalOn(t, later, paths.Home, true)
		if v, prompt := asking(t, later, laterOut, laterTyped, creating(paths.Home, scheduled), "y"); !v.Allow || prompt == "" {
			t.Errorf("an expired entry read %+v, prompt %q", v, prompt)
		}
	})

	t.Run("a trigger on delivered events always asks", func(t *testing.T) {
		v, prompt := asking(t, e, out, typed, creating(paths.Home, events), "r", "y")
		if !v.Allow || !strings.Contains(prompt, "(on delivered events)") || strings.Contains(prompt, "[r]emember") {
			t.Errorf("an events trigger read %+v with:\n%s", v, prompt)
		}
		if v, prompt := asking(t, e, out, typed, creating(paths.Home, events), "y"); !v.Allow || prompt == "" {
			t.Errorf("an events trigger was not asked again: %+v", v)
		}
		for _, r := range remembered() {
			if r.Trigger == "trg_2" {
				t.Errorf("an events trigger was remembered: %+v", r)
			}
		}
	})

	t.Run("with no terminal anything not remembered is refused", func(t *testing.T) {
		alone := promptEngine(t, paths, func() time.Time { return clock })
		quiet, _ := terminalOn(t, alone, paths.Home, false)
		for _, labels := range []map[string]string{person, events} {
			if v := alone.Admit(t.Context(), creating(paths.Home, labels)); v.Allow || v.Reason != ReasonNoOneHere {
				t.Errorf("an unattended create read %+v", v)
			}
		}
		if v := alone.Admit(t.Context(), creating(paths.Home, scheduled)); !v.Allow {
			t.Errorf("a remembered trigger with no terminal read %+v", v)
		}
		if strings.Contains(quiet.String(), "Allow this session?") {
			t.Errorf("a terminal that is not interactive asked:\n%s", quiet.String())
		}
	})
}

// Under session_start allow, a session a person started and one a scheduled
// trigger started run with no one asked, each told to the person in one
// line, in the activity log and as an event; a trigger on delivered events,
// or one whose firing is unnamed, still asks; a switch to ask in the file
// applies to the next create.
func TestSessionStartAllow(t *testing.T) {
	paths := testPaths(t)
	connected(t, paths, OpenKeys(paths.KeyFile()))
	setStart := func(mode string) {
		t.Helper()
		p, _, err := LoadPolicy(paths.PolicyFile())
		if err != nil {
			t.Fatal(err)
		}
		p.SessionStart = mode
		if err = SavePolicy(paths.PolicyFile(), p); err != nil {
			t.Fatal(err)
		}
	}
	// A file from before session_start existed: no field, which reads as
	// allow.
	raw, err := os.ReadFile(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	withoutField := strings.Replace(string(raw), "session_start: ask\n", "", 1)
	if withoutField == string(raw) {
		t.Fatalf("the file names no session_start to remove:\n%s", raw)
	}
	if err = os.WriteFile(paths.PolicyFile(), []byte(withoutField), 0o600); err != nil {
		t.Fatal(err)
	}
	e := promptEngine(t, paths, time.Now)
	events, stop := e.Subscribe()
	defer stop()
	out, typed := terminalOn(t, e, paths.Home, true)
	person := map[string]string{LabelSession: "ses_p", LabelAgent: "agt_1", LabelVersion: "4"}
	scheduled := map[string]string{LabelSession: "ses_s", LabelAgent: "agt_1", LabelVersion: "4", LabelTrigger: "trg_1", LabelFiresOn: FiresOnSchedule}
	delivered := map[string]string{LabelSession: "ses_e", LabelAgent: "agt_1", LabelVersion: "4", LabelTrigger: "trg_2", LabelFiresOn: FiresOnEvents}
	unnamed := map[string]string{LabelSession: "ses_u", LabelAgent: "agt_1", LabelVersion: "4", LabelTrigger: "trg_3"}
	// told waits for the started event and the approval line of session.
	told := func(session, answer string) {
		t.Helper()
		var started, logged bool
		deadline := time.After(5 * time.Second)
		for !started || !logged {
			select {
			case ev := <-events:
				switch {
				case ev.Kind == EventStarted && ev.Approval.Session == session:
					started = true
				case ev.Kind == EventActivity && ev.Activity.Type == TypeApproval && ev.Activity.Session == session:
					if ev.Activity.Answer != answer {
						t.Errorf("the activity log reads %q for %s, want %q", ev.Activity.Answer, session, answer)
					}
					logged = true
				}
			case <-deadline:
				t.Fatalf("%s: started event %v, activity line %v", session, started, logged)
			}
		}
	}

	if v, prompt := asking(t, e, out, typed, creating(paths.Home, person)); !v.Allow || prompt != "" {
		t.Errorf("a session a person started read %+v, prompt %q", v, prompt)
	}
	told("ses_p", answerSessionStart)
	waitFor(t, "the line for ses_p", func() bool {
		return strings.Contains(out.String(), "Site helper (ses_p, started by a person) is using ~/project (read-write).")
	})
	if v, _ := asking(t, e, out, typed, creating(paths.Home, scheduled)); !v.Allow {
		t.Errorf("a scheduled trigger read %+v", v)
	}
	told("ses_s", answerSessionStart)
	waitFor(t, "the line for ses_s", func() bool {
		return strings.Contains(out.String(), "Site helper (ses_s, started by trigger trg_1 on a schedule) is using ~/project (read-write).")
	})
	if strings.Contains(out.String(), "A session wants to use this computer.") {
		t.Errorf("a session that starts without asking was asked:\n%s", out.String())
	}

	// A trigger on delivered events asks, and so does one whose firing is
	// unnamed; once allowed, it is told like the others.
	for _, labels := range []map[string]string{delivered, unnamed} {
		v, prompt := asking(t, e, out, typed, creating(paths.Home, labels), "y")
		if !v.Allow || prompt == "" {
			t.Errorf("%s read %+v without a prompt", labels[LabelSession], v)
		}
		told(labels[LabelSession], AnswerAllow)
	}
	// With no one attached, a trigger on delivered events is refused at once.
	alone := promptEngine(t, paths, time.Now)
	if v := alone.Admit(t.Context(), creating(paths.Home, delivered)); v.Allow || v.Reason != ReasonNoOneHere {
		t.Errorf("an unattended trigger on delivered events read %+v", v)
	}
	if v := alone.Admit(t.Context(), creating(paths.Home, person)); !v.Allow {
		t.Errorf("an unattended session a person started read %+v", v)
	}

	// A switch to ask applies to the next create, with the engine running.
	setStart(SessionStartAsk)
	if v, prompt := asking(t, e, out, typed, creating(paths.Home, person), "n"); v.Allow || prompt == "" {
		t.Errorf("under ask, a session a person started read %+v without a prompt", v)
	}
	setStart(SessionStartAllow)
	if v, prompt := asking(t, e, out, typed, creating(paths.Home, person)); !v.Allow || prompt != "" {
		t.Errorf("back under allow, a session a person started read %+v, prompt %q", v, prompt)
	}
	// A file that names another device is not this computer's say: asks.
	p, _, err := LoadPolicy(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	p.Device = "dev-anotherdeviceaaaa"
	if err = SavePolicy(paths.PolicyFile(), p); err != nil {
		t.Fatal(err)
	}
	if v, prompt := asking(t, e, out, typed, creating(paths.Home, person), "n"); v.Allow || prompt == "" {
		t.Errorf("with a file naming another device, a session read %+v without a prompt", v)
	}
}

func TestTerminalShowsTheEngine(t *testing.T) {
	paths := testPaths(t)
	connected(t, paths, OpenKeys(paths.KeyFile()))
	e := promptEngine(t, paths, time.Now)
	out, typed := terminalOn(t, e, paths.Home, true)
	e.setState(StateOnline, "")
	e.log(Entry{Time: time.Now(), Type: "Exec", Session: "ses_x", Command: []string{"go", "test"}})
	e.log(Entry{Time: time.Now(), Type: "Inspect", Session: "ses_x"})
	e.notice("the key is in a file")
	e.mu.Lock()
	e.sessions = 2
	status := e.statusLocked()
	e.mu.Unlock()
	e.hub.publish(Event{Kind: EventStatus, Status: &status})
	e.setState(StateOffline, "the connection ended")
	e.setState(StatePaused, "")
	waitFor(t, "every line", func() bool { return strings.Contains(out.String(), "Paused.") })
	got := out.String()
	for _, want := range []string{
		"Online. No session is using this computer.", "ses_x  exec  go test", "Note: the key is in a file",
		"Online. 2 sessions are using this computer.", "Offline: the connection ended.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the terminal lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "inspect") {
		t.Errorf("the terminal printed a bookkeeping operation:\n%s", got)
	}

	// An answer from another client moves the terminal on.
	verdict := make(chan worker.Verdict, 1)
	go func() {
		verdict <- e.Admit(context.Background(), creating(paths.Home, map[string]string{LabelSession: "ses_o"}))
	}()
	waitFor(t, "the prompt", func() bool { return len(e.Waiting()) == 1 && strings.Contains(out.String(), "ses_o") })
	if err := e.Answer(e.Waiting()[0].ID, AnswerDeny, "Latere for Mac"); err != nil {
		t.Fatal(err)
	}
	if v := <-verdict; v.Allow {
		t.Error("a refusal from another client admitted the create")
	}
	waitFor(t, "the other client's answer", func() bool { return strings.Contains(out.String(), "Refused in Latere for Mac.") })
	// A line typed with nothing waiting is ignored. The empty line after it
	// is read only once the terminal took the y, so the y cannot answer the
	// create below; the empty line answers nothing wherever it lands.
	if _, err := io.WriteString(typed, "y\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(typed, "\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	withdrawn := make(chan struct{})
	go func() {
		e.Admit(ctx, creating(paths.Home, map[string]string{LabelSession: "ses_w"}))
		close(withdrawn)
	}()
	waitFor(t, "the prompt", func() bool { return len(e.Waiting()) == 1 })
	cancel()
	<-withdrawn
	waitFor(t, "the withdrawal", func() bool { return strings.Contains(out.String(), "The session stopped waiting for an answer.") })
}

func TestPromptWording(t *testing.T) {
	for _, tc := range []struct {
		a    Approval
		want string
	}{
		{Approval{}, "agent    an unnamed agent"},
		{Approval{Agent: "agt_1", AgentRef: "agt_1", AgentVersion: "2"}, "agent    agt_1 (version 2)"},
		{Approval{Agent: "Helper", AgentRef: "agt_1"}, "agent    Helper (agt_1)"},
		{Approval{}, "session  not named"},
		{Approval{}, "folder   none; the session works in its own scratch directory"},
		{Approval{Folder: "/h/notes"}, "folder   ~/notes (read-only)"},
	} {
		if got := Prompt(tc.a, "/h"); !strings.Contains(got, tc.want) {
			t.Errorf("Prompt(%+v) lacks %q:\n%s", tc.a, tc.want, got)
		}
	}
	for answer, want := range map[string]string{
		AnswerAllow: "Allowed in x.", AnswerRemember: "Allowed and remembered for 30 days in x.",
		AnswerDeny: "Refused in x.", answerNoOne: "Refused: no one was left to answer.", "other": "Answered in x.",
	} {
		if got := answeredElsewhere(answer, "x"); got != want {
			t.Errorf("answeredElsewhere(%s) = %q", answer, got)
		}
	}
	if statusLine(Status{State: StateOnline, Sessions: 1}) != "Online. 1 session is using this computer." ||
		statusLine(Status{State: StateOffline}) != "Offline. Reconnecting." || statusLine(Status{}) != "Connecting." {
		t.Error("a status line is misworded")
	}
	if ParseAnswer(" YES ", false) != AnswerAllow || ParseAnswer("no", true) != AnswerDeny || ParseAnswer("remember", true) != AnswerRemember ||
		ParseAnswer("r", false) != "" || ParseAnswer("maybe", true) != "" {
		t.Error("an answer is misread")
	}
	if !strings.Contains(answerHint(true), "r to remember") {
		t.Error("the hint for a rememberable create does not name r")
	}
}

func TestConnectScreenWording(t *testing.T) {
	policy := Policy{
		Name: "MacBook Pro", Folders: []Folder{{Path: "~/code/site", Write: true}, {Path: "~/notes"}},
		Network: Network{Mode: NetworkAllowlist, Hosts: []string{"github.com", "pypi.org"}},
	}
	common := []string{
		`"MacBook Pro"`, "~/code/site (read-write)", "~/notes (read-only)", ".env", "github.com, pypi.org",
		"can carry data out", "session's log and to\nthe model", "organization can\nread it too", "Type yes",
		"still approved in the chat,\nunless you approved a matching one before",
	}
	// A computer connected with no session_start, the default, starts the
	// person's own sessions without asking, and says who else that reaches.
	allow := ConnectScreen(policy)
	for _, want := range append(common,
		"Sessions you start, and your triggers that run on a schedule, start on\nthis computer without asking here.",
		"mail or a\nwebhook asks here first", "Anyone who can start sessions as you can reach these folders",
		"never your credentials", "latere devices policy --session-start ask",
	) {
		if !strings.Contains(allow, want) {
			t.Errorf("the connect screen lacks %q:\n%s", want, allow)
		}
	}
	policy.SessionStart = SessionStartAsk
	ask := ConnectScreen(policy)
	for _, want := range append(common[:len(common)-1],
		"Each session asks for your approval here before it starts.", "also approved in the chat",
		"latere devices policy --session-start allow",
	) {
		if !strings.Contains(ask, want) {
			t.Errorf("the connect screen under ask lacks %q:\n%s", want, ask)
		}
	}
	if strings.Contains(ask, "without asking here.") {
		t.Errorf("the connect screen under ask says sessions start without asking:\n%s", ask)
	}
	if none := ConnectScreen(Policy{Network: Network{Mode: NetworkNone}}); !strings.Contains(none, "Network: none") {
		t.Errorf("a computer with no network reads:\n%s", none)
	}
}

func TestStartedLineAndNotice(t *testing.T) {
	for _, tc := range []struct {
		a    Approval
		want string
	}{
		{Approval{Agent: "Site helper", Session: "ses_1", Folder: "/h/code/site", Write: true},
			"Site helper (ses_1, started by a person) is using ~/code/site (read-write)."},
		{Approval{Agent: "Site helper", Session: "ses_2", Trigger: "trg_1", FiresOn: FiresOnSchedule, Folder: "/h/notes"},
			"Site helper (ses_2, started by trigger trg_1 on a schedule) is using ~/notes (read-only)."},
		{Approval{Agent: "Site helper", Session: "ses_3", Trigger: "trg_2", FiresOn: FiresOnEvents, Folder: "/h/notes"},
			"Site helper (ses_3, started by trigger trg_2 on delivered events) is using ~/notes (read-only)."},
		{Approval{Trigger: "trg_3"},
			"An unnamed agent (a session not named, started by trigger trg_3) is using its own scratch directory."},
	} {
		if got := StartedLine(tc.a, "/h"); got != tc.want {
			t.Errorf("StartedLine = %q, want %q", got, tc.want)
		}
	}
	if allow := SessionStartNotice(Policy{}); !strings.Contains(allow, "start on this computer without asking here") ||
		!strings.Contains(allow, "--session-start ask") || strings.Contains(allow, "\n") {
		t.Errorf("the notice for a file with no session_start reads %q", allow)
	}
	if ask := SessionStartNotice(Policy{SessionStart: SessionStartAsk}); !strings.Contains(ask, "Each session asks") ||
		!strings.Contains(ask, "--session-start allow") {
		t.Errorf("the notice under ask reads %q", ask)
	}
}
