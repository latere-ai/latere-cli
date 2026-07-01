// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"latere.ai/x/topos/models"
)

// handleLocalCommand runs a slash command typed at the `latere topos --local`
// prompt. It returns quit=true when the session should end. rebuild swaps the
// runner's model (used by /model); curModel points at the header's model label.
func handleLocalCommand(ctx context.Context, line string, curModel *string, rebuild func(models.Model) error) (quit bool) {
	cmd := strings.Fields(line)[0]
	switch cmd {
	case "/quit", "/exit":
		return true
	case "/help":
		printLocalHelp()
	case "/model":
		switchLocalModel(ctx, strings.TrimSpace(strings.TrimPrefix(line, cmd)), curModel, rebuild)
	default:
		fmt.Printf("unknown command %q — /help for the list\n", cmd)
	}
	return false
}

func printLocalHelp() {
	fmt.Println(strings.TrimSpace(`
Commands:
  /model [name]   switch model — no name opens a picker of your Lux models
  /help           show this help
  /quit, /exit    leave (or press Ctrl+D)
`))
}

// switchLocalModel changes the active model. With no name it opens a picker over
// the Anthropic models Lux exposes to this identity; with a name it switches
// directly (works for any provider, e.g. an Ollama tag).
func switchLocalModel(ctx context.Context, name string, curModel *string, rebuild func(models.Model) error) {
	target := name
	if target == "" {
		list, err := fetchLuxModels(ctx)
		if err != nil || len(list) == 0 {
			fmt.Println(styleDim.Render("could not list Lux models; use /model <name>"))
			return
		}
		chosen, perr := runModelPicker(ctx, list, *curModel)
		if perr != nil || chosen == "" {
			return
		}
		target = chosen
	}
	b, err := buildLocalModel(ctx, target)
	if err != nil {
		fmt.Println(styleErr.Render("switch failed: " + err.Error()))
		return
	}
	if err := rebuild(b); err != nil {
		fmt.Println(styleErr.Render(err.Error()))
		return
	}
	fmt.Printf("switched to %s\n", *curModel)
}

// fetchLuxModels returns the Anthropic model ids Lux exposes to this identity
// and marks enabled — the models usable through --local's Anthropic-via-Lux
// path (see `latere lux models`).
func fetchLuxModels(ctx context.Context) ([]string, error) {
	c, _, err := luxClient(ctx, "", "", "")
	if err != nil {
		return nil, err
	}
	var resp luxCatalogResponse
	if err := c.GetJSON(ctx, "/lux/v1/models", &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range resp.Items {
		if s, _ := it["status"].(string); s != "enabled" {
			continue
		}
		if p, _ := it["provider"].(string); p != "anthropic" {
			continue
		}
		if m, _ := it["model"].(string); m != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

// modelPicker is a minimal single-select list over model ids, marking the one
// currently in use.
type modelPicker struct {
	models  []string
	cursor  int
	current string
	chosen  string
}

func newModelPicker(list []string, current string) modelPicker {
	mp := modelPicker{models: list, current: current}
	for i, m := range list {
		if m == current {
			mp.cursor = i
		}
	}
	return mp
}

func (m modelPicker) Init() tea.Cmd { return nil }

func (m modelPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.models)-1 {
			m.cursor++
		}
	case "enter":
		m.chosen = m.models[m.cursor]
		return m, tea.Quit
	}
	return m, nil
}

func (m modelPicker) View() string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Select a model") + "\n\n")
	for i, name := range m.models {
		cursor, label := "  ", name
		if name == m.current {
			label += styleDim.Render("  (current)")
		}
		if i == m.cursor {
			cursor = "▸ "
			label = lipgloss.NewStyle().Bold(true).Render(name)
			if name == m.current {
				label += styleDim.Render("  (current)")
			}
		}
		b.WriteString(cursor + label + "\n")
	}
	b.WriteString("\n" + styleDim.Render("[↑↓] move   [enter] select   [q] cancel") + "\n")
	return b.String()
}

// runModelPicker shows the picker and returns the chosen model id, or "" if the
// user cancelled.
func runModelPicker(ctx context.Context, list []string, current string) (string, error) {
	p := tea.NewProgram(newModelPicker(list, current), tea.WithContext(ctx))
	fm, err := p.Run()
	if err != nil {
		return "", err
	}
	return fm.(modelPicker).chosen, nil
}
