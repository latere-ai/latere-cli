// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

// Package deviceterm is the terminal client of the device engine that
// `latere devices connect` and `serve` run: the status line, a line per
// operation and notice, and the person's answer to each session typed in
// the terminal.
package deviceterm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"latere.ai/x/cella/device"
)

// terminalClient is how an answer typed in the terminal names its client.
const terminalClient = "this terminal"

// answerNoOne is the answer the engine gives a waiting create when the last
// client that could answer it detached.
const answerNoOne = "no_one_here"

// Engine is what the terminal asks of the engine it is a client of;
// *device.Engine is one.
type Engine interface {
	Subscribe() (<-chan device.Event, func())
	Attach() (detach func())
	Status() device.Status
	Answer(id, answer, by string) error
}

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
	Engine Engine
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
	var waiting []device.Approval
	last := ""
	// A serve that waits to be connected may have said so before this
	// terminal subscribed; it says it once here.
	if s := t.Engine.Status(); s.State == device.StateNotConnected {
		last = statusLine(s)
		t.print(last)
	}
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
			case device.EventStatus:
				if line := statusLine(*ev.Status); line != last {
					last = line
					t.print(line)
				}
			case device.EventActivity:
				if !slices.Contains(quietTypes, ev.Activity.Type) {
					t.print(ev.Activity.Line(t.Home))
				}
			case device.EventNotice:
				t.print("Note: " + ev.Notice)
			case device.EventStarted:
				t.print(StartedLine(*ev.Approval, t.Home))
			case device.EventAsked:
				if !t.Interactive {
					continue
				}
				waiting = append(waiting, *ev.Approval)
				if len(waiting) == 1 {
					t.print(Prompt(waiting[0], t.Home))
				}
			case device.EventAnswered, device.EventWithdrawn:
				i := slices.IndexFunc(waiting, func(a device.Approval) bool { return a.ID == ev.Approval.ID })
				if i < 0 {
					continue
				}
				if i == 0 {
					switch {
					case ev.Kind == device.EventWithdrawn:
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
func StartedLine(a device.Approval, home string) string {
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
		folder = device.Shorten(home, a.Folder) + " (read-write)"
	case a.Folder != "":
		folder = device.Shorten(home, a.Folder) + " (read-only)"
	}
	return fmt.Sprintf("%s (%s, %s) is using %s.", agent, session, a.StartedWords(), folder)
}

// Prompt is the question a create puts to the person.
func Prompt(a device.Approval, home string) string {
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
		fmt.Fprintf(&b, "  folder   %s (read-write)\n", device.Shorten(home, a.Folder))
	default:
		fmt.Fprintf(&b, "  folder   %s (read-only)\n", device.Shorten(home, a.Folder))
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
		return device.AnswerAllow
	case "n", "no":
		return device.AnswerDeny
	case "r", "remember":
		if rememberable {
			return device.AnswerRemember
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
	case device.AnswerAllow:
		return "Allowed in " + by + "."
	case device.AnswerRemember:
		return "Allowed and remembered for 30 days in " + by + "."
	case device.AnswerDeny:
		return "Refused in " + by + "."
	case answerNoOne:
		return "Refused: no one was left to answer."
	}
	return "Answered in " + by + "."
}

// statusLine is the status as the terminal prints it.
func statusLine(s device.Status) string {
	switch s.State {
	case device.StateOnline:
		switch s.Sessions {
		case 0:
			return "Online. No session is using this computer."
		case 1:
			return "Online. 1 session is using this computer."
		}
		return fmt.Sprintf("Online. %d sessions are using this computer.", s.Sessions)
	case device.StateOffline:
		if s.Detail != "" {
			return "Offline: " + s.Detail + "."
		}
		return "Offline. Reconnecting."
	case device.StatePaused:
		return "Paused. Sessions cannot use this computer until it resumes."
	case device.StateNotConnected:
		return "Not connected. Waiting for a client to connect this computer through the control socket."
	}
	return "Connecting."
}
