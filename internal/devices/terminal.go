// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package devices

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
)

// terminalClient is how an answer typed in the terminal names its client.
const terminalClient = "this terminal"

// quietTypes are operations the terminal does not print a line for: they
// read the sandbox's state for the control plane and name no command or
// path. The activity log still records them.
var quietTypes = []string{"Inspect", "List", "Watch", "Touch"}

// Terminal is the engine's client in the terminal `latere devices connect`
// runs in: it prints the status line, a line per operation and the notices,
// and asks the person about each create. A terminal that is not interactive
// prints and asks nothing, so a create waits for another client or is
// refused.
type Terminal struct {
	Engine *Engine
	In     io.Reader
	Out    io.Writer
	// Interactive is whether a person can answer at In.
	Interactive bool
	// Home shortens the paths it prints.
	Home string
}

// Run serves the terminal until ctx ends.
func (t Terminal) Run(ctx context.Context) {
	events, unsubscribe := t.Engine.Subscribe()
	defer func() { unsubscribe() }()
	if t.Interactive {
		detach := t.Engine.Attach()
		defer detach()
	}
	lines := make(chan string)
	if t.Interactive {
		go func() {
			scanner := bufio.NewScanner(t.In)
			for scanner.Scan() {
				select {
				case lines <- scanner.Text():
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	var waiting []Approval
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// A terminal that fell behind is dropped by the engine; it
				// takes a new subscription and goes on.
				events, unsubscribe = t.Engine.Subscribe()
				continue
			}
			switch ev.Kind {
			case EventStatus:
				if line := statusLine(*ev.Status); line != last {
					last = line
					t.print(line)
				}
			case EventActivity:
				if !slices.Contains(quietTypes, ev.Activity.Type) {
					t.print(ev.Activity.Line(t.Home))
				}
			case EventNotice:
				t.print("Note: " + ev.Notice)
			case EventStarted:
				t.print(StartedLine(*ev.Approval, t.Home))
			case EventAsked:
				if !t.Interactive {
					continue
				}
				waiting = append(waiting, *ev.Approval)
				if len(waiting) == 1 {
					t.print(Prompt(waiting[0], t.Home))
				}
			case EventAnswered, EventWithdrawn:
				i := slices.IndexFunc(waiting, func(a Approval) bool { return a.ID == ev.Approval.ID })
				if i < 0 {
					continue
				}
				if i == 0 {
					switch {
					case ev.Kind == EventWithdrawn:
						t.print("The session stopped waiting for an answer.")
					case ev.By != terminalClient:
						t.print(answeredElsewhere(ev.Answer, ev.By))
					}
				}
				waiting = slices.Delete(waiting, i, i+1)
				if i == 0 && len(waiting) > 0 {
					t.print(Prompt(waiting[0], t.Home))
				}
			}
		case line := <-lines:
			if len(waiting) == 0 {
				continue
			}
			a := waiting[0]
			answer := ParseAnswer(line, a.Rememberable)
			if answer == "" {
				t.print(answerHint(a.Rememberable))
				continue
			}
			if err := t.Engine.Answer(a.ID, answer, terminalClient); err != nil {
				t.print("That answer was not taken: " + err.Error() + ".")
			}
		}
	}
}

func (t Terminal) print(line string) { _, _ = fmt.Fprintln(t.Out, line) }

// StartedLine is the line that tells the person a session started on the
// computer: the agent, the session and who started it, and the folder.
func StartedLine(a Approval, home string) string {
	agent := a.Agent
	if agent == "" {
		agent = "An unnamed agent"
	}
	session := a.Session
	if session == "" {
		session = "a session not named"
	}
	folder := "its own scratch directory"
	switch {
	case a.Folder != "" && a.Write:
		folder = Shorten(home, a.Folder) + " (read-write)"
	case a.Folder != "":
		folder = Shorten(home, a.Folder) + " (read-only)"
	}
	return fmt.Sprintf("%s (%s, %s) is using %s.", agent, session, a.startedWords(), folder)
}

// The sentences that tell the person how sessions start here, and how to
// change it, at every start and on the connect screen.
const (
	sessionStartAllowWords = "Sessions you start, and your triggers that run on a schedule, start on\n" +
		"this computer without asking here. A trigger that runs on mail or a\n" +
		"webhook asks here first, since anyone with its address can send it input.\n" +
		"Each command that changes something is still approved in the chat,\n" +
		"unless you approved a matching one before."
	sessionStartAskWords = "Each session asks for your approval here before it starts. Each command\n" +
		"that changes something is also approved in the chat, unless you approved\n" +
		"a matching one before."
	switchToAsk   = "To be asked here before every session starts, run\n`latere devices policy --session-start ask`."
	switchToAllow = "To let your own sessions and scheduled triggers start without asking\nhere, run `latere devices policy --session-start allow`."
)

// SessionStartNotice is what the engine says at every start about how
// sessions start, so a computer connected before session_start existed
// learns the default it now has.
func SessionStartNotice(p Policy) string {
	if p.SessionStart == SessionStartAsk {
		return strings.ReplaceAll(sessionStartAskWords+" "+switchToAllow, "\n", " ")
	}
	return strings.ReplaceAll(sessionStartAllowWords+" "+switchToAsk, "\n", " ")
}

// Prompt is the question a create puts to the person.
func Prompt(a Approval, home string) string {
	var b strings.Builder
	b.WriteString("A session wants to use this computer.\n")
	agent := a.Agent
	switch {
	case agent == "":
		agent = "an unnamed agent"
	case a.AgentRef != "" && a.AgentRef != agent && a.AgentVersion != "":
		agent += " (" + a.AgentRef + ", version " + a.AgentVersion + ")"
	case a.AgentRef != "" && a.AgentRef != agent:
		agent += " (" + a.AgentRef + ")"
	case a.AgentVersion != "":
		agent += " (version " + a.AgentVersion + ")"
	}
	fmt.Fprintf(&b, "  agent    %s\n", agent)
	session := a.Session
	if session == "" {
		session = "not named"
	}
	fmt.Fprintf(&b, "  session  %s\n", session)
	fmt.Fprintf(&b, "  started  %s\n", a.StartedBy())
	switch {
	case a.Folder == "":
		b.WriteString("  folder   none; the session works in its own scratch directory\n")
	case a.Write:
		fmt.Fprintf(&b, "  folder   %s (read-write)\n", Shorten(home, a.Folder))
	default:
		fmt.Fprintf(&b, "  folder   %s (read-only)\n", Shorten(home, a.Folder))
	}
	if a.Rememberable {
		b.WriteString("Remembering lets this trigger run here with no one asked, until its agent changes or 30 days pass.\n")
		b.WriteString("Allow this session? [y]es / [n]o / [r]emember this trigger for 30 days")
	} else {
		b.WriteString("Allow this session? [y]es / [n]o")
	}
	return b.String()
}

// ParseAnswer reads a typed answer, and "" for one that is none; remember
// is an answer only for a create that can be remembered.
func ParseAnswer(line string, rememberable bool) string {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return AnswerAllow
	case "n", "no":
		return AnswerDeny
	case "r", "remember":
		if rememberable {
			return AnswerRemember
		}
	}
	return ""
}

func answerHint(rememberable bool) string {
	if rememberable {
		return "Type y to allow, n to refuse, or r to remember this trigger for 30 days."
	}
	return "Type y to allow or n to refuse."
}

// answeredElsewhere tells the terminal that another client answered.
func answeredElsewhere(answer, by string) string {
	switch answer {
	case AnswerAllow:
		return "Allowed in " + by + "."
	case AnswerRemember:
		return "Allowed and remembered for 30 days in " + by + "."
	case AnswerDeny:
		return "Refused in " + by + "."
	case answerNoOne:
		return "Refused: no one was left to answer."
	}
	return "Answered in " + by + "."
}

// statusLine is the status as the terminal prints it.
func statusLine(s Status) string {
	switch s.State {
	case StateOnline:
		switch s.Sessions {
		case 0:
			return "Online. No session is using this computer."
		case 1:
			return "Online. 1 session is using this computer."
		}
		return fmt.Sprintf("Online. %d sessions are using this computer.", s.Sessions)
	case StateOffline:
		if s.Detail != "" {
			return "Offline: " + s.Detail + "."
		}
		return "Offline. Reconnecting."
	case StatePaused:
		return "Paused. Sessions cannot use this computer until it resumes."
	}
	return "Connecting."
}

// ConnectScreen is what the person is shown before a computer is
// connected: what sessions may use, what the agent reads, and who may ask.
func ConnectScreen(p Policy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Connect this computer to Latere as %q?\n\n", p.Name)
	b.WriteString("Sessions you start in Latere will run commands and read and edit files\non this computer, in these folders only:\n")
	for _, f := range p.Folders {
		access := "read-only"
		if f.Write {
			access = "read-write"
		}
		fmt.Fprintf(&b, "  %s (%s)\n", f.Path, access)
	}
	b.WriteString("Everything in these folders is readable to the agent, a project's .env\nfile and other secrets kept there included.\n\n")
	if p.Network.Mode == NetworkNone || len(p.Network.Hosts) == 0 {
		b.WriteString("Network: none. Commands on this computer reach no host.\n\n")
	} else {
		b.WriteString("Network: commands may reach these hosts only:\n")
		fmt.Fprintf(&b, "  %s\n", strings.Join(p.Network.Hosts, ", "))
		b.WriteString("A host that accepts writes, such as a git host, can carry data out.\n\n")
	}
	b.WriteString("Only sessions you start, and triggers you own, can use this computer.\n")
	if p.SessionStart == SessionStartAsk {
		b.WriteString(sessionStartAskWords + "\n" + switchToAllow + "\n\n")
	} else {
		b.WriteString(sessionStartAllowWords + "\n")
		b.WriteString("Anyone who can start sessions as you can reach these folders, still\n" +
			"confined to them: never your credentials, keychains or browser profiles,\n" +
			"and no network beyond what is listed above.\n")
		b.WriteString(switchToAsk + "\n\n")
	}
	b.WriteString("What the agent reads on this computer goes to the session's log and to\nthe model. In an organization's conversation, that organization can\nread it too.\n\n")
	b.WriteString("Type yes to connect this computer: ")
	return b.String()
}
