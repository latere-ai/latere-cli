// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// defaultLoginScopes is the scope set `latere topos` requests when it signs a
// user in (mirrors `latere auth login`); run:agents lets the token drive Topos.
const defaultLoginScopes = "openid email profile offline_access read:sandbox write:sandbox exec:sandbox attach:sandbox llm.read llm.invoke llm.serve run:agents read:agents write:agents"

// runToposHome is the `latere topos` landing experience: sign in if needed, then
// loop on the home screen — start a new session, or resume one — until quit. A
// session is just the agent loop running as you; there is no agent to create.
func runToposHome(ctx context.Context, apiURL string) error {
	if err := ensureToposLogin(ctx, apiURL); err != nil {
		return err
	}
	for {
		c, err := toposClient(apiURL)
		if err != nil {
			return err
		}
		var sresp listSessionsResponse
		if err := c.GetJSON(ctx, "/v1/sessions", &sresp); err != nil {
			return err
		}

		// Non-interactive terminal (piped/CI): print a plain summary.
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			return printHomeText(sresp.Sessions)
		}

		p := tea.NewProgram(newHomeModel(sresp.Sessions), tea.WithContext(ctx), tea.WithAltScreen())
		fm, err := p.Run()
		if err != nil {
			return err
		}
		switch res := fm.(homeModel).result; res.action {
		case homeQuit:
			return nil
		case homeRefresh:
			continue
		case homeAttach:
			if err := runInteractiveSession(ctx, c, res.sessionID, false); err != nil {
				return err
			}
		case homeStart:
			// Start an ephemeral, owner-scoped session: no agent_id, no setup.
			var created interactiveSessionDTO
			if err := c.PostJSON(ctx, "/v1/sessions", map[string]string{}, &created); err != nil {
				return err
			}
			if err := runInteractiveSession(ctx, c, created.ID, false); err != nil {
				return err
			}
		}
	}
}

// ensureToposLogin signs the user in (device flow) when there is no usable token,
// so `latere topos` never dead-ends on a "run latere auth login" error.
func ensureToposLogin(ctx context.Context, apiURL string) error {
	if os.Getenv("TOPOS_TOKEN") != "" {
		return nil
	}
	if _, err := toposIdentityBearer(); err == nil {
		return nil
	}
	fmt.Fprintln(os.Stderr, "Sign in to Topos to continue.")
	return runDeviceFlow(ctx, deviceFlowOpts{
		ClientID: "latere-cli",
		AuthURL:  toposAuthBase(),
		Scopes:   defaultLoginScopes,
	})
}

// printHomeText is the non-TTY fallback: a plain listing of sessions.
func printHomeText(sessions []interactiveSessionDTO) error {
	if len(sessions) == 0 {
		fmt.Fprintln(os.Stdout, "No sessions yet. Run `latere topos` in a terminal to start one.")
		return nil
	}
	fmt.Fprintln(os.Stdout, "Sessions:")
	for _, s := range sessions {
		fmt.Fprintf(os.Stdout, "  %s  %s\n", s.ID, friendlyStatus(s.Status)) //nolint:errcheck
	}
	return nil
}

// homeAction is what the home screen returns when it exits.
type homeAction int

const (
	homeQuit homeAction = iota
	homeRefresh
	homeAttach // resume an existing session (homeResult.sessionID)
	homeStart  // start a new ephemeral session
)

type homeResult struct {
	action    homeAction
	sessionID string
}

// homeRow is one selectable line: the "new session" action, or a session to
// resume.
type homeRow struct {
	newSession bool
	sessionID  string
	title      string
	detail     string // shown dim
	group      string // section heading
}

// homeModel is the `latere topos` landing screen: start a new session, or resume
// one. It is a picker: it exits with a homeResult the command acts on.
type homeModel struct {
	rows   []homeRow
	cursor int
	result homeResult
	width  int
	height int
}

// buildHomeRows puts "New session" first (the primary action), then sessions
// grouped by what they need (input, running, recent).
func buildHomeRows(sessions []interactiveSessionDTO) []homeRow {
	rank := func(s string) int {
		switch s {
		case "awaiting_approval":
			return 0
		case "awaiting_input":
			return 1
		case "running":
			return 2
		default:
			return 3
		}
	}
	groupOf := func(s string) string {
		switch s {
		case "awaiting_approval", "awaiting_input":
			return "Needs your input"
		case "running":
			return "Running"
		default:
			return "Recent"
		}
	}
	ss := append([]interactiveSessionDTO(nil), sessions...)
	sort.SliceStable(ss, func(i, j int) bool {
		if rank(ss[i].Status) != rank(ss[j].Status) {
			return rank(ss[i].Status) < rank(ss[j].Status)
		}
		return ss[i].CreatedAt > ss[j].CreatedAt
	})

	rows := []homeRow{{newSession: true, title: "New session", detail: "start coding", group: "Start"}}
	for _, s := range ss {
		rows = append(rows, homeRow{
			sessionID: s.ID, title: "Session",
			detail: friendlyStatus(s.Status) + "  " + shortID(s.ID), group: groupOf(s.Status),
		})
	}
	return rows
}

func friendlyStatus(s string) string {
	switch s {
	case "awaiting_input":
		return "waiting for you"
	case "awaiting_approval":
		return "needs approval"
	case "running":
		return "working"
	case "completed":
		return "done"
	case "failed":
		return "failed"
	default:
		return s
	}
}

func newHomeModel(sessions []interactiveSessionDTO) homeModel {
	return homeModel{rows: buildHomeRows(sessions), width: 80, height: 24}
}

func (m homeModel) Init() tea.Cmd { return nil }

func (m homeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.result = homeResult{action: homeQuit}
			return m, tea.Quit
		case "r":
			m.result = homeResult{action: homeRefresh}
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.rows)-1 {
				m.cursor++
			}
		case "enter":
			if m.cursor < len(m.rows) {
				row := m.rows[m.cursor]
				if row.newSession {
					m.result = homeResult{action: homeStart}
				} else {
					m.result = homeResult{action: homeAttach, sessionID: row.sessionID}
				}
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

var (
	homeTitle = lipgloss.NewStyle().Bold(true)
	homeGroup = lipgloss.NewStyle().Bold(true).Faint(true)
	homeSel   = lipgloss.NewStyle().Bold(true)
	homeDim   = lipgloss.NewStyle().Faint(true)
)

func (m homeModel) View() string {
	var b strings.Builder
	b.WriteString(homeTitle.Render("Topos") + "  " + homeDim.Render("your coding sessions") + "\n\n")

	lastGroup := ""
	for i, row := range m.rows {
		if row.group != lastGroup {
			if lastGroup != "" {
				b.WriteString("\n")
			}
			b.WriteString(homeGroup.Render(row.group) + "\n")
			lastGroup = row.group
		}
		cursor := "  "
		title := row.title
		if i == m.cursor {
			cursor = "▸ "
			title = homeSel.Render(row.title)
		}
		b.WriteString(cursor + title + "   " + homeDim.Render(row.detail) + "\n")
	}

	b.WriteString("\n" + homeDim.Render("[enter] open   [↑↓] move   [r] refresh   [q] quit") + "\n")
	return b.String()
}
