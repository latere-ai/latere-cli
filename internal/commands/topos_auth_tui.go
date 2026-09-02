// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The auth picker shown by `latere topos --local` when no model credential is
// configured: choose a provider and sign in / enter a key, like opencode's auth
// flow. Only providers whose SDK adapter actually works are offered (Anthropic,
// Ollama); OpenAI/Gemini are pending their adapters.

type authChoice int

const (
	authCancel authChoice = iota
	authClaude            // Claude OAuth (browser)
	authAPIKey            // Anthropic API key
	authOllama            // local Ollama
)

type authResult struct {
	choice authChoice
	apiKey string
}

type authOption struct {
	label  string
	choice authChoice
}

type authModel struct {
	options  []authOption
	cursor   int
	entering bool // true while typing an API key
	input    textinput.Model
	result   authResult
}

func newAuthModel() authModel {
	in := textinput.New()
	in.Placeholder = "sk-ant-api03-..."
	in.Prompt = "API key: "
	return authModel{
		options: []authOption{
			{"Sign in with Claude (opens your browser)", authClaude},
			{"Use an Anthropic API key", authAPIKey},
			{"Use Ollama (local models, no key)", authOllama},
		},
		input: in,
	}
}

func (m authModel) Init() tea.Cmd { return nil }

func (m authModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.entering {
		switch key.Type {
		case tea.KeyEnter:
			m.result = authResult{choice: authAPIKey, apiKey: strings.TrimSpace(m.input.Value())}
			return m, tea.Quit
		case tea.KeyEsc:
			m.entering = false
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch key.String() {
	case "ctrl+c", "q", "esc":
		m.result = authResult{choice: authCancel}
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.options)-1 {
			m.cursor++
		}
	case "enter":
		sel := m.options[m.cursor].choice
		if sel == authAPIKey {
			m.entering = true
			m.input.Focus()
			return m, textinput.Blink
		}
		m.result = authResult{choice: sel}
		return m, tea.Quit
	}
	return m, nil
}

var (
	authTitle = lipgloss.NewStyle().Bold(true)
	authSel   = lipgloss.NewStyle().Bold(true)
	authDim   = lipgloss.NewStyle().Faint(true)
)

func (m authModel) View() string {
	var b strings.Builder
	b.WriteString(authTitle.Render("Sign in to Topos") + "  " + authDim.Render("choose a model provider") + "\n\n")
	if m.entering {
		b.WriteString(m.input.View() + "\n\n")
		b.WriteString(authDim.Render("[enter] save   [esc] back") + "\n")
		return b.String()
	}
	for i, o := range m.options {
		cursor := "  "
		label := o.label
		if i == m.cursor {
			cursor = "▸ "
			label = authSel.Render(o.label)
		}
		b.WriteString(cursor + label + "\n")
	}
	b.WriteString("\n" + authDim.Render("[↑↓] move   [enter] select   [q] cancel") + "\n")
	return b.String()
}

// runAuthPicker shows the provider picker and applies the choice: a Claude OAuth
// login, a stored Anthropic API key, or Ollama. It returns an error if the user
// cancels or the chosen step fails.
func runAuthPicker(ctx context.Context) error {
	p := tea.NewProgram(newAuthModel(), tea.WithContext(ctx))
	fm, err := p.Run()
	if err != nil {
		return err
	}
	// Run returns the model it was given, so this is the authModel above.
	res := fm.(authModel).result //nolint:errcheck // tea.Program.Run returns the same model it was started with
	switch res.choice {
	case authClaude:
		return loopbackClaudeLogin(ctx)
	case authAPIKey:
		if res.apiKey == "" {
			return errors.New("no API key entered")
		}
		return saveProviderConfig(providerConfig{Provider: "anthropic", Method: "apikey", APIKey: res.apiKey})
	case authOllama:
		return saveProviderConfig(providerConfig{Provider: "ollama"})
	default:
		return errors.New("sign-in cancelled")
	}
}
