// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tuiSender is the control-message sink the TUI writes to (a frameStream in
// production, a fake in tests).
type tuiSender interface {
	Send(attachControl) error
}

// streamEventMsg carries one frameStream message into the bubbletea loop. ok is
// false when the stream channel has closed (the session is gone).
type streamEventMsg struct {
	m  streamMsg
	ok bool
}

// tuiModel is the interactive client UI: a scrollback transcript, a status bar,
// and an input box (or an approve/deny prompt). It renders a sessionState that
// it folds frames into, and sends control messages through sender.
type tuiModel struct {
	state     *sessionState
	input     textinput.Model
	events    <-chan streamMsg
	sender    tuiSender
	readonly  bool
	sessionID string
	width     int
	height    int
	quitting  bool
}

func newTUIModel(sessionID string, events <-chan streamMsg, sender tuiSender, readonly bool) tuiModel {
	in := textinput.New()
	in.Placeholder = "Type a message, Enter to send, Esc to interrupt, Ctrl+C to quit"
	in.Prompt = "› "
	if !readonly {
		in.Focus()
	}
	return tuiModel{
		state:     newSessionState(),
		input:     in,
		events:    events,
		sender:    sender,
		readonly:  readonly,
		sessionID: sessionID,
		width:     80,
		height:    24,
	}
}

// waitEvent reads the next stream message as a bubbletea Msg.
func waitEvent(ch <-chan streamMsg) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-ch
		return streamEventMsg{m: m, ok: ok}
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, waitEvent(m.events))
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case streamEventMsg:
		if !msg.ok {
			m.state.status = "closed"
			m.quitting = true
			return m, tea.Quit
		}
		if msg.m.frame != nil {
			m.state.apply(*msg.m.frame)
			if m.state.status == "closed" {
				m.quitting = true
				return m, tea.Quit
			}
		} else if msg.m.note != "" {
			m.state.status = msg.m.note
		}
		return m, waitEvent(m.events)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = msg.Width - 4
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if !m.readonly {
			if err := m.sender.Send(attachControl{Type: "interrupt"}); err != nil {
				m.state.lines = append(m.state.lines, "⚠ could not send interrupt: "+err.Error())
			}
		}
		return m, nil
	case tea.KeyEnter:
		return m.handleEnter()
	}
	// An outstanding approval: y/n decides it (when read-write).
	if m.state.pending != nil && !m.readonly {
		switch decision := strings.ToLower(msg.String()); decision {
		case "y", "n":
			ctrl := attachControl{Type: "approval_reply", DecisionID: m.state.pending.DecisionID, Approve: decision == "y"}
			if err := m.sender.Send(ctrl); err != nil {
				m.state.lines = append(m.state.lines, "⚠ could not send approval: "+err.Error())
				return m, nil
			}
			m.state.resolvePending()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m tuiModel) handleEnter() (tea.Model, tea.Cmd) {
	if m.readonly {
		return m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	if err := m.sender.Send(attachControl{Type: "user_turn", Text: text}); err != nil {
		m.state.lines = append(m.state.lines, "⚠ could not send message: "+err.Error())
		return m, nil
	}
	m.state.echoUser(text)
	m.input.Reset()
	return m, nil
}

var (
	statusStyle = lipgloss.NewStyle().Bold(true)
	hintStyle   = lipgloss.NewStyle().Faint(true)
)

func (m tuiModel) View() string {
	if m.quitting {
		return "session " + m.sessionID + " (" + m.state.status + ")\nDetached. Reattach with: latere topos session attach " + m.sessionID + "\n"
	}

	// Transcript tail that fits above the status + input rows.
	body := m.state.lines
	if live := m.state.liveText(); live != "" {
		body = append(append([]string{}, body...), "● "+live)
	}
	reserve := 3
	visible := max(m.height-reserve, 1)
	if len(body) > visible {
		body = body[len(body)-visible:]
	}

	var b strings.Builder
	b.WriteString(strings.Join(body, "\n"))
	b.WriteString("\n")
	bar := statusStyle.Render("["+m.state.status+"]") + "  " + hintStyle.Render(m.sessionID)
	if m.state.usage != "" {
		bar += "  " + hintStyle.Render(m.state.usage)
	}
	b.WriteString(bar)
	b.WriteString("\n")
	if m.state.pending != nil && !m.readonly {
		b.WriteString("approve tool " + m.state.pending.ToolID + "? [y/n]")
	} else if m.readonly {
		b.WriteString(hintStyle.Render("read-only viewer"))
	} else {
		b.WriteString(m.input.View())
	}
	return b.String()
}
