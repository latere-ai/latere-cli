// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/agents"
	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/oversight"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The hosted-session commands of agents read the signed-in person's hosted
// sessions and how each tool call of one was decided (spec 024), through
// the Latere Go SDK's agents package. Like the oversight commands, they are
// the person's own debugging tool; nothing here is given to an agent
// inside a session.

// defaultAgentsURL is the agents API at the platform origin, its base path
// included.
const defaultAgentsURL = latere.DefaultBaseURL + agents.BasePath

// The environment that overrides the agents API's address and bearer.
const (
	envAgentsURL   = "LATERE_AGENTS_URL"
	envAgentsToken = "LATERE_AGENTS_TOKEN"
)

// agentsTimeout bounds one request, its retries included.
const agentsTimeout = 30 * time.Second

// agentsURLUsage is the --api-url help the hosted-session commands share.
const agentsURLUsage = "agents API base URL, including its /v1/agents path (default " + defaultAgentsURL + ", or $" + envAgentsURL + ")"

// agentsClient is one command's connection to the agents API.
type agentsClient struct {
	ag   *agents.Client
	base string
}

// newAgentsClient resolves the API's address, the flag, then
// $LATERE_AGENTS_URL, then the platform origin, and its bearer:
// LATERE_AGENTS_TOKEN as given, else actor tokens minted from the saved
// login.
func newAgentsClient(flagURL, authURL string) (*agentsClient, error) {
	base := strings.TrimRight(cmp.Or(flagURL, os.Getenv(envAgentsURL), defaultAgentsURL), "/")
	origin, ok := strings.CutSuffix(base, agents.BasePath)
	if !ok {
		return nil, refusedf("%s is not an agents API base URL: it ends in %s, as %s does", base, agents.BasePath, defaultAgentsURL)
	}
	cfg, err := api.Product{
		BaseURL: origin, AuthURL: authURL, Audience: latere.DefaultAudience,
		Token: strings.TrimSpace(os.Getenv(envAgentsToken)), Timeout: agentsTimeout,
	}.Config()
	if err != nil {
		return nil, refusedf("%w", err)
	}
	return &agentsClient{ag: agents.New(cfg), base: base}, nil
}

// refused is what a command says of a call that failed, with its exit
// code, as apiRefused words it for the agents API.
func (c *agentsClient) refused(err error) error {
	return apiRefused("agents", c.base, err)
}

// ---- sessions ----

func newAgentsSessionsCmd() *cobra.Command {
	var (
		apiURL, authURL string
		limit           int
		jsonF           bool
	)
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List your recent hosted sessions.",
		Long: `List your recent hosted sessions, newest first: each one's id, its agent,
its title, its status, when it started, and the approval mode its tool
calls are decided under. 'latere agents session <id>' shows how each tool
call of one was decided.

The commands call https://api.latere.ai/v1/agents with a token minted for
your login. LATERE_AGENTS_URL or --api-url overrides the address, and
LATERE_AGENTS_TOKEN presents a bearer as given. They exit 0 when they are
done, 2 when the request was refused and running it again gives the same
answer, and 3 when no answer came and running it again may succeed.`,
		Example: `  latere agents sessions
  latere agents sessions --limit 3 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 0 {
				return refusedf("--limit is %d; give 0 for every session, or more", limit)
			}
			c, err := newAgentsClient(apiURL, authURL)
			if err != nil {
				return err
			}
			sessions, err := c.sessions(cmd.Context(), limit)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, raws(sessions))
			}
			return printSessions(out, sessions)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", agentsURLUsage)
	cmd.Flags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the API URL)")
	cmd.Flags().IntVar(&limit, "limit", 20, "list this many sessions, newest first; 0 lists them all")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the sessions as JSON")
	return cmd
}

// sessions reads the person's sessions, newest first, up to limit of them,
// or all when limit is zero.
func (c *agentsClient) sessions(ctx context.Context, limit int) ([]*agents.Session, error) {
	params := &agents.ListSessionsParams{Limit: 500}
	if limit > 0 {
		params.Limit = min(limit, 500)
	}
	out := []*agents.Session{}
	for s, err := range c.ag.ListSessions(ctx, params) {
		if err != nil {
			return nil, c.refused(err)
		}
		out = append(out, s)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// sessionMode is the approval mode a session's calls are decided under, or
// "-" for one that leaves them to its agent.
func sessionMode(s *agents.Session) string {
	if s.Policy == nil {
		return "-"
	}
	return dash(string(s.Policy.Mode))
}

// sessionStatus is a session's status, with why it stopped.
func sessionStatus(s *agents.Session) string {
	if s.StopReason == "" {
		return string(s.Status)
	}
	return fmt.Sprintf("%s (%s)", s.Status, s.StopReason)
}

func printSessions(out io.Writer, sessions []*agents.Session) error {
	if len(sessions) == 0 {
		fprintln(out, "You have no hosted sessions.")
		return nil
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fprintln(w, "ID\tAGENT\tTITLE\tSTATUS\tSTARTED\tMODE")
	for _, s := range sessions {
		fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, dash(s.Agent.Name), dash(clip(oneLine(s.Title), 48)), sessionStatus(s), formatTime(s.CreatedAt), sessionMode(s))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fprint(out, stripANSI(b.String()))
	return nil
}

// clip shortens s to at most n runes, marking a cut with an ellipsis.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---- session ----

// toolCall is one tool call of a session and the person's answer to it,
// as `latere agents session --json` prints it: the call's event, its
// action id in Limen, the call as the API recorded it, and the
// confirmation that answered it, if one did.
type toolCall struct {
	Seq      int64           `json:"seq"`
	Time     time.Time       `json:"time"`
	Subagent string          `json:"subagent,omitempty"`
	ActionID string          `json:"action_id"`
	ToolUse  json.RawMessage `json:"tool_use"`
	Answer   json.RawMessage `json:"answer,omitempty"`

	use    agents.ToolUse
	answer *agents.ToolConfirmation
}

func newAgentsSessionCmd() *cobra.Command {
	var (
		apiURL, authURL string
		jsonF           bool
	)
	cmd := &cobra.Command{
		Use:   "session <id>",
		Short: "Show how each tool call of a hosted session was decided.",
		Long: `Show a hosted session's tool calls in order, and how each was decided: the
tool and a short summary of its input, the verdict and the approval mode,
the layer of the rules that decided it and the rule within it, its risk
score and the rule set that scored it, what Limen suggested (the verdict,
the chance you allow the call, how unsure it is, and for an ask whether
it was unsure or split, if the session recorded it), the chance you were
shown the call, whether a person began its turn, and your answer to it,
if you gave one. A session from before the platform recorded why Limen
asks shows none.

The first lines name the organization that owns the session's agent, if
one does. A call that Limen keeps in an organization's context, not in
yours, names that organization.

The oversight commands call each tool call a step. 'latere oversight
explain <id>/<tool_use_id>' says why Limen suggested what it did for a
step, and 'latere oversight cases --session <id>' lists the session's
steps that Limen holds, with your checks of them. Both read each step in
the organization this command names for it.

--json prints the session as the API answered it, with each tool call's
event and the confirmation that answered it.`,
		Example: `  latere agents session ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX
  latere agents session ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			c, err := newAgentsClient(apiURL, authURL)
			if err != nil {
				return err
			}
			s, err := c.ag.GetSession(cmd.Context(), id)
			if err != nil {
				return c.refused(err)
			}
			calls, err := c.toolCalls(cmd.Context(), s.ID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, map[string]any{"session": s.RawJSON(), "tool_calls": calls})
			}
			return printSessionCalls(out, s, calls)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", agentsURLUsage)
	cmd.Flags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the API URL)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the session and its tool calls as JSON")
	return cmd
}

// toolCalls reads the session's whole log and returns its tool calls in
// order, each with the person's confirmation of it, the latest when there
// are several.
func (c *agentsClient) toolCalls(ctx context.Context, id string) ([]*toolCall, error) {
	calls := []*toolCall{}
	byID := map[string]*toolCall{}
	for e, err := range c.ag.ListEvents(ctx, id, nil) {
		if err != nil {
			return nil, c.refused(err)
		}
		switch e.Type {
		case agents.EventAgentToolUse:
			tc := &toolCall{Seq: e.Seq, Time: e.Time, Subagent: e.Subagent, ToolUse: e.Payload}
			if err := e.Decode(&tc.use); err != nil {
				return nil, &exitError{code: exitNoAnswer, err: err}
			}
			tc.ActionID = oversight.ActionID(id, tc.use.ToolUseID)
			calls = append(calls, tc)
			byID[tc.use.ToolUseID] = tc
		case agents.EventUserToolConfirmation:
			var conf agents.ToolConfirmation
			if err := e.Decode(&conf); err != nil {
				return nil, &exitError{code: exitNoAnswer, err: err}
			}
			if tc := byID[conf.ToolUseID]; tc != nil && conf.ToolUseID != "" {
				tc.answer, tc.Answer = &conf, e.Payload
			}
		}
	}
	return calls, nil
}

func printSessionCalls(out io.Writer, s *agents.Session, calls []*toolCall) error {
	var b strings.Builder
	fprintf(&b, "%s  %s", s.ID, dash(s.Agent.Name))
	if s.Title != "" {
		fprintf(&b, "  %s", oneLine(s.Title))
	}
	fprintf(&b, "\n%s, started %s, mode %s", sessionStatus(s), formatTime(s.CreatedAt), sessionMode(s))
	if s.Org != "" {
		fprintf(&b, ", agent of organization %s", s.Org)
	}
	fprint(&b, "\n")
	if len(calls) == 0 {
		fprint(&b, "\nThe session made no tool calls.\n")
		fprint(out, stripANSI(b.String()))
		return nil
	}
	for i, tc := range calls {
		u := tc.use
		fprintf(&b, "\n%d. %s", i+1, u.Name)
		if sum := inputSummary(u.Name, u.Input); sum != "" {
			fprintf(&b, "  %s", sum)
		}
		fprintf(&b, "  [%s]", u.ToolUseID)
		if tc.Subagent != "" {
			fprintf(&b, "  (subagent %s)", tc.Subagent)
		}
		fprintf(&b, "\n   %s", dash(string(u.Verdict)))
		if u.Mode != "" {
			fprintf(&b, " in %s mode", u.Mode)
		}
		if u.DecidedBy != "" {
			fprintf(&b, ", decided by %s", u.DecidedBy)
			if u.DecidedByRule != "" {
				fprintf(&b, ": %s", u.DecidedByRule)
			}
		}
		var facts []string
		if r := u.Risk; r != nil {
			facts = append(facts, fmt.Sprintf("risk %.2f (%s)", r.Score, dash(r.Source)))
		}
		if p := u.ReviewProbability; p != nil {
			facts = append(facts, "shown "+percent(*p))
		}
		if a := u.Attended; a != nil {
			if *a {
				facts = append(facts, "attended")
			} else {
				facts = append(facts, "unattended")
			}
		}
		if len(facts) > 0 {
			fprintf(&b, "\n   %s", strings.Join(facts, ", "))
		}
		if sg := u.Suggestion; sg != nil {
			fprintf(&b, "\n   Limen suggested %s, %s allow, uncertainty %.2f", dash(string(sg.Verdict)), percent(sg.Approve), sg.Uncertainty)
			if sg.AskKind != "" {
				fprintf(&b, ", %s", sg.AskKind)
			}
			fprintf(&b, " (%s)", dash(sg.Source))
		}
		if isOrganization(u.DecisionOrg) {
			fprintf(&b, "\n   Limen keeps it in organization %s", u.DecisionOrg)
		}
		if a := tc.answer; a != nil {
			fprintf(&b, "\n   answered %s", a.Decision)
			if who := cmp.Or(a.Sender.Name, a.Sender.Subject); who != "" {
				fprintf(&b, " by %s", who)
			}
			if a.Remember != "" {
				fprintf(&b, ", remembering %s", a.Remember)
			}
		}
		fprint(&b, "\n")
	}
	fprintf(&b, "\nWhy Limen suggested what it did: latere oversight explain %s/<tool_use_id>\n", s.ID)
	fprintf(&b, "Your checks of these calls:      latere oversight cases --session %s\n", s.ID)
	fprint(out, stripANSI(b.String()))
	return nil
}

// inputSummary is a short line of a call's input: the command of a shell
// call, the path or address a file or web call names, or else the input's
// first string field, cut to fit.
func inputSummary(name string, input json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	for _, key := range []string{"command", "file_path", "path", "url", "query", "pattern", "description"} {
		if v, ok := fields[key].(string); ok && v != "" {
			return clip(oneLine(v), 72)
		}
	}
	if name == "question" {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if v, ok := fields[k].(string); ok && v != "" {
			return clip(oneLine(k+": "+v), 72)
		}
	}
	return ""
}
