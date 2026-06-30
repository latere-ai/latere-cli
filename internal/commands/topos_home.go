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
const defaultLoginScopes = "openid email profile offline_access read:sandbox write:sandbox exec:sandbox attach:sandbox llm.read llm.invoke llm.serve run:agents"

// runToposHome is the `latere topos` landing experience: it signs the user in if
// needed, then loops on the home screen — pick a session to resume, or an agent
// to start a new one — until they quit.
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
		var aresp listAgentsResponse
		if err := c.GetJSON(ctx, "/v1/agents", &aresp); err != nil {
			return err
		}

		// Non-interactive terminal (piped/CI): print a plain summary instead of a
		// TUI that needs a TTY.
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			return printHomeText(sresp.Sessions, aresp.Agents)
		}

		p := tea.NewProgram(newHomeModel(sresp.Sessions, aresp.Agents), tea.WithContext(ctx), tea.WithAltScreen())
		fm, err := p.Run()
		if err != nil {
			return err
		}
		res := fm.(homeModel).result
		switch res.action {
		case homeQuit:
			return nil
		case homeRefresh:
			continue
		case homeAttach:
			if err := runInteractiveSession(ctx, c, res.sessionID, false); err != nil {
				return err
			}
		case homeStart:
			var created interactiveSessionDTO
			if err := c.PostJSON(ctx, "/v1/sessions", map[string]string{"agent_id": res.agentID}, &created); err != nil {
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

// printHomeText is the non-TTY fallback: a plain listing of sessions + agents.
func printHomeText(sessions []interactiveSessionDTO, agents []agentDTO) error {
	if len(sessions) == 0 && len(agents) == 0 {
		fmt.Fprintln(os.Stdout, "No agents or sessions yet.")
		return nil
	}
	if len(sessions) > 0 {
		fmt.Fprintln(os.Stdout, "Sessions:")
		for _, s := range sessions {
			fmt.Fprintf(os.Stdout, "  %s  %-16s  %s\n", s.ID, friendlyStatus(s.Status), s.AgentID) //nolint:errcheck
		}
	}
	if len(agents) > 0 {
		fmt.Fprintln(os.Stdout, "Agents:")
		for _, a := range agents {
			fmt.Fprintf(os.Stdout, "  %s  %s\n", a.ID, a.DisplayName) //nolint:errcheck
		}
	}
	return nil
}

// homeAction is what the home screen returns when it exits.
type homeAction int

const (
	homeQuit homeAction = iota
	homeRefresh
	homeAttach // open an existing session (homeResult.sessionID)
	homeStart  // start a new session on an agent (homeResult.agentID)
)

type homeResult struct {
	action    homeAction
	sessionID string
	agentID   string
}

// homeRow is one selectable line: an existing session or an agent to start.
type homeRow struct {
	isAgent bool
	id      string // session id or agent id
	title   string // agent name
	detail  string // status / id, shown dim
	group   string // section heading this row belongs to
}

// homeModel is the `latere topos` landing screen: a single navigable list of
// sessions (grouped by what they need) and agents you can start. It is a picker:
// it exits with a homeResult the command acts on, so it composes with the
// session UI without nesting bubbletea programs.
type homeModel struct {
	rows   []homeRow
	cursor int
	result homeResult
	width  int
	height int
}

// buildHomeRows turns sessions + agents into grouped, ordered rows. Sessions are
// grouped by urgency (needs input, running, recent); agents follow.
func buildHomeRows(sessions []interactiveSessionDTO, agents []agentDTO) []homeRow {
	name := map[string]string{}
	for _, a := range agents {
		name[a.ID] = a.DisplayName
	}
	agentLabel := func(id string) string {
		if n := name[id]; n != "" {
			return n
		}
		return id
	}

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

	var rows []homeRow
	for _, s := range ss {
		rows = append(rows, homeRow{
			id: s.ID, title: agentLabel(s.AgentID),
			detail: friendlyStatus(s.Status) + "  " + shortID(s.ID),
			group:  groupOf(s.Status),
		})
	}
	for _, a := range agents {
		rows = append(rows, homeRow{
			isAgent: true, id: a.ID, title: a.DisplayName,
			detail: a.Kind, group: "Start a new session",
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

func newHomeModel(sessions []interactiveSessionDTO, agents []agentDTO) homeModel {
	return homeModel{rows: buildHomeRows(sessions, agents), width: 80, height: 24}
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
				if row.isAgent {
					m.result = homeResult{action: homeStart, agentID: row.id}
				} else {
					m.result = homeResult{action: homeAttach, sessionID: row.id}
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
	b.WriteString(homeTitle.Render("Topos") + "  " + homeDim.Render("your agent sessions") + "\n\n")

	if len(m.rows) == 0 {
		b.WriteString(homeDim.Render("No agents or sessions yet.\n"))
		b.WriteString("\n" + homeDim.Render("[r] refresh   [q] quit") + "\n")
		return b.String()
	}

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
		line := "  " + row.title + "   " + homeDim.Render(row.detail)
		if i == m.cursor {
			cursor = "▸ "
			line = homeSel.Render("  "+row.title) + "   " + homeDim.Render(row.detail)
		}
		b.WriteString(cursor + strings.TrimPrefix(line, "  ") + "\n")
	}

	b.WriteString("\n" + homeDim.Render("[enter] open / start   [↑↓] move   [r] refresh   [q] quit") + "\n")
	return b.String()
}
