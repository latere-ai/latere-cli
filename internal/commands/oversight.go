// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/pkg/authkit/jwt"
	"latere.ai/x/sdk-go/agents"
	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/oversight"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The oversight commands read what Limen, the platform's decision service,
// recorded and learned about the signed-in person, and let them correct it
// (spec 024), through the Latere Go SDK's oversight package. They are the
// person's own debugging tool, and their coding agent's on their machine;
// nothing here is given to an agent inside a session.

// defaultOversightURL is the oversight API at the platform origin, its base
// path included.
const defaultOversightURL = latere.DefaultBaseURL + oversight.BasePath

// The environment that overrides the oversight API's address and bearer.
const (
	envOversightURL   = "LATERE_OVERSIGHT_URL"
	envOversightToken = "LATERE_OVERSIGHT_TOKEN"
)

// oversightTimeout bounds one request, its retries included.
const oversightTimeout = 30 * time.Second

// oversightURLUsage is the --api-url help every oversight command shares.
const oversightURLUsage = "oversight API base URL, including its /v1/oversight path (default " + defaultOversightURL + ", or $" + envOversightURL + ")"

func newOversightCmd() *cobra.Command {
	var apiURL, authURL string
	cmd := &cobra.Command{
		Use:   "oversight",
		Short: "See what Limen recorded and learned about you, and correct it.",
		Long: `See what Limen, the decision service that learns when to ask you about an
agent's step, recorded and learned about you, and correct it.

  latere oversight cases                    the steps it holds, newest last
  latere oversight cases --pending          the steps waiting for you to check
  latere oversight explain <action>         why it suggested what it did
  latere oversight learned                  what it learned, per kind of step
  latere oversight reliability              how often it went against you
  latere oversight calibration              how its chances compare with your checks
  latere oversight profile                  your trust profile
  latere oversight profile set balanced     choose careful, balanced or strict
  latere oversight profile --agent <name>   an agent's own profile, or the one it follows
  latere oversight profile set strict --for-me
                                            make your organization's ask you more
  latere oversight report                   your organization's weeks, for owners and admins
  latere oversight answer <action> allow    allow or deny a step you checked
  latere oversight forget                   remove everything it holds about you

A step is one tool call of an agent in a session. An <action> names a
step: the session's id and the step's tool use id with a / between them,
as 'latere agents session <id>' and 'latere oversight cases' print it.

Limen keeps your records in a context. Your own context is your subject:
your issuer and your sign-in id with a | between them. The steps of an
organization's agent can be in the organization's context. 'explain',
'answer' and 'cases --session' read the session's log and use the
organization each step was decided under, or your own context for a step
that names none. 'profile --agent' reads an agent's profile in the context
your login acts in, and 'profile --for-me' and 'report' the organization
your login acts in. The other commands read your own context. Limen answers
for an organization's context only to a login in that organization:
'latere login --org-id <id>'.

The commands call https://api.latere.ai/v1/oversight with a token minted
for your login. LATERE_OVERSIGHT_URL or --api-url overrides the address,
and LATERE_OVERSIGHT_TOKEN presents a bearer as given. Each command exits
0 when it is done, 2 when the request was refused and running it again
gives the same answer, and 3 when no answer came and running it again may
succeed.`,
		Example: `  latere oversight cases --pending
  latere oversight explain ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01
  latere oversight learned --json
  latere oversight profile set careful`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&apiURL, "api-url", "", oversightURLUsage)
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the API URL)")
	cmd.AddCommand(
		newOversightCasesCmd(&apiURL, &authURL),
		newOversightExplainCmd(&apiURL, &authURL),
		newOversightLearnedCmd(&apiURL, &authURL),
		newOversightReliabilityCmd(&apiURL, &authURL),
		newOversightCalibrationCmd(&apiURL, &authURL),
		newOversightProfileCmd(&apiURL, &authURL),
		newOversightReportCmd(&apiURL, &authURL),
		newOversightAnswerCmd(&apiURL, &authURL),
		newOversightForgetCmd(&apiURL, &authURL),
	)
	return cmd
}

// ---- the client ----

// oversightClient is one command's connection to the oversight API: the
// SDK's client, the address as the command was given it, which messages
// name, the person every call is about, and the auth URL the agents API is
// read with to find the organization of a session's steps.
type oversightClient struct {
	ov   *oversight.Client
	base string
	// me is the person's subject: the subject of every call, and the org
	// of every call in the person's own context.
	me string
	// org is the organization the bearer acts in, its id, or "" for the
	// person's own context: the context of an agent's own profile, of the
	// person's stricter profile and of the report.
	org string
	// authURL is the --auth-url the command was given.
	authURL string
}

// newOversightClient resolves the API's address, the flag, then
// $LATERE_OVERSIGHT_URL, then the platform origin; its bearer,
// LATERE_OVERSIGHT_TOKEN as given, else actor tokens minted from the saved
// login; and the person, from the bearer the commands present.
func newOversightClient(ctx context.Context, flagURL, authURL string) (*oversightClient, error) {
	base := strings.TrimRight(cmp.Or(flagURL, os.Getenv(envOversightURL), defaultOversightURL), "/")
	origin, ok := strings.CutSuffix(base, oversight.BasePath)
	if !ok {
		return nil, refusedf("%s is not an oversight API base URL: it ends in %s, as %s does", base, oversight.BasePath, defaultOversightURL)
	}
	token := strings.TrimSpace(os.Getenv(envOversightToken))
	me, org, err := signedInSubject(ctx, origin, authURL, token)
	if err != nil {
		return nil, err
	}
	cfg, err := api.Product{
		BaseURL: origin, AuthURL: authURL, Audience: latere.DefaultAudience,
		Token: token, Timeout: oversightTimeout,
	}.Config()
	if err != nil {
		return nil, refusedf("%w", err)
	}
	return &oversightClient{ov: oversight.New(cfg), base: base, me: me, org: org, authURL: authURL}, nil
}

// sessionSteps is the organization Limen holds each step of a hosted
// session under, by tool use id, with the steps in the log's order. A
// step's organization is the decision_org its agent.tool_use recorded
// (topos spec 100): an organization's id when the installation keys an
// organization's agent's calls by the organization, and otherwise the
// initiator's subject. A step that recorded none, as every step did before
// Topos recorded it, is in the person's own context.
type sessionSteps struct {
	org   map[string]string
	order []string
}

// orgOf is the organization of the step toolUseID: an organization's id
// as the log names it, and the person's own subject for a step the log
// names a person's context for, or none. A person's context is read as the
// person's own subject, as the command renders it, so a core that renders
// the subject another way reads as every step did before a core named one.
func (st sessionSteps) orgOf(toolUseID, me string) string {
	if org := st.org[toolUseID]; isOrganization(org) {
		return org
	}
	return me
}

// orgs are the organizations the session's steps are in, the person's own
// first and each other once, in the order the log names them.
func (st sessionSteps) orgs(me string) []string {
	out := []string{me}
	for _, id := range st.order {
		if org := st.orgOf(id, me); !slices.Contains(out, org) {
			out = append(out, org)
		}
	}
	return out
}

// steps reads the session's log through the agents API for the
// organization of each of its steps. A session the agents API does not
// have, such as a step's of another installation, has every step in the
// person's own context. So does a log that cannot be read, with a note on
// errOut: the command then reads the person's own records, as it did before
// a step named its organization.
func (c *oversightClient) steps(ctx context.Context, errOut io.Writer, session string) sessionSteps {
	st := sessionSteps{org: map[string]string{}}
	ag, err := newAgentsClient("", c.authURL)
	if err == nil {
		for e, ierr := range ag.ag.ListEvents(ctx, session, nil) {
			if ierr != nil {
				err = ierr
				break
			}
			if e.Type != agents.EventAgentToolUse {
				continue
			}
			var u agents.ToolUse
			if derr := e.Decode(&u); derr != nil || u.ToolUseID == "" {
				continue
			}
			if _, seen := st.org[u.ToolUseID]; !seen {
				st.order = append(st.order, u.ToolUseID)
			}
			st.org[u.ToolUseID] = u.DecisionOrg
		}
	}
	if err != nil && !errors.Is(err, latere.ErrNotFound) {
		fprintf(errOut, "Could not read session %s to find the organization of its steps, so your own records are read: %v\n", session, err)
		return sessionSteps{org: map[string]string{}}
	}
	return st
}

// orgOfAction is the organization Limen holds the step id names under, as
// steps reads it from the step's session.
func (c *oversightClient) orgOfAction(ctx context.Context, errOut io.Writer, id string) string {
	session, tool, _ := strings.Cut(id, "/")
	return c.steps(ctx, errOut, session).orgOf(tool, c.me)
}

// signedInSubject is the person the commands act for, rendered as Limen
// keys them: their issuer and their sign-in id with a | between them, and
// the organization the bearer acts in, "" for the person's own context. It
// reads them from token when that is a JWT, and otherwise from the saved
// login, whose issuer and organization the actor tokens minted from it
// share. An issuer the token does not name is the one the login resolves
// to.
func signedInSubject(ctx context.Context, origin, authURL, token string) (subject, org string, err error) {
	raw, issuer := token, api.ResolveAuthURL(origin, authURL)
	if _, err := principalFromJWT(raw); raw == "" || err != nil {
		access, authBase, err := api.LoginToken(ctx, issuer)
		if err != nil {
			return "", "", refusedf("find who you are signed in as: %w", err)
		}
		raw, issuer = access, authBase
	}
	var claims struct {
		Iss   string `json:"iss"`
		Sub   string `json:"sub"`
		OrgID string `json:"org_id"`
	}
	if err := jwt.DecodePayload(raw, &claims); err != nil || claims.Sub == "" {
		return "", "", refusedf("the saved login names no subject; run `latere login`")
	}
	return oversight.Subject(cmp.Or(claims.Iss, issuer), claims.Sub), claims.OrgID, nil
}

// refused is what a command says of a call that failed, with its exit
// code, as apiRefused words it for the oversight API, after naming the
// refusals a person meets with what to do about them.
func (c *oversightClient) refused(err error) error {
	return c.refusedIn(c.me, err)
}

// refusedIn is refused for a call in the context org: a refusal in an
// organization's context names the login Limen answers it to.
func (c *oversightClient) refusedIn(org string, err error) error {
	switch {
	case errors.Is(err, oversight.ErrNoDecision):
		return refusedf("Limen has no decision recorded for that step, so there is nothing to answer yet")
	case errors.Is(err, oversight.ErrAskTooCostly):
		return refusedf("those costs would let Limen decide a step of yours without ever asking, so they are refused")
	case errors.Is(err, latere.ErrForbidden) && isOrganization(org):
		return refusedf("that step is in organization %s, and Limen answers for it only to a login in that organization: run `latere login --org-id %s`", org, org)
	}
	return apiRefused("oversight", c.base, err)
}

// isOrganization reports whether org names an organization's context: an
// organization's id has no |, and a person's subject has one.
func isOrganization(org string) bool {
	return org != "" && !strings.Contains(org, "|")
}

// parseActionID reads <session>/<tool_use_id>.
func parseActionID(arg string) (string, error) {
	session, tool, ok := strings.Cut(strings.TrimSpace(arg), "/")
	if !ok || session == "" || tool == "" || strings.Contains(tool, "/") {
		return "", refusedf("%q is not <session>/<tool_use_id>, for example ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01", arg)
	}
	return oversight.ActionID(session, tool), nil
}

// ---- the words ----

// percent is a probability as a person reads it: 0.55 is "55%".
func percent(p float64) string {
	return fmt.Sprintf("%.0f%%", 100*p)
}

// signed is a value with its sign, to two places.
func signed(v float64) string {
	return fmt.Sprintf("%+.2f", v)
}

// askWhy says why Limen asks about the step an explanation explains:
// unsure, too few answers yet; split, answers that went both ways on the
// step's kind; short, answers that agree, with the chance and the
// threshold it falls short of, the run threshold from one half up and the
// stop threshold below it, as the console says it; wider, a reach no
// approval of the person's covers; and few, a reach they allowed with too
// little weight or in too few sessions. The profile is the preset nearest
// the thresholds the explanation was made under.
//
// A Limen that sends a cover splits only on answers that disagree. An
// older Limen calls an ask split by its evidence alone, so its split is
// split in the commands' words only where the answers that moved it went
// both ways, and short otherwise.
//
// hidden is the words that stand in for the chance, as chanceRule gives
// them, and "" where the chance may be stated: a short ask then states no
// chance and no threshold, since a threshold says which side the chance
// is on.
func askWhy(e *oversight.Explanation, hidden string) string {
	split := e.AskKind == oversight.AskSplit && (e.Cover != nil || bothWays(e.Evidence))
	switch {
	case e.AskKind == oversight.AskUnsure:
		return "unsure: your answers say little about steps like this yet, so more answers will move it"
	case split:
		return "split: your answers about steps like this went both ways, so more answers will not move it much"
	case e.AskKind == oversight.AskShort, e.AskKind == oversight.AskSplit:
		return shortWhy(e, hidden)
	case e.AskKind == oversight.AskWider:
		return "wider: this step reaches further than any step of its kind that you allowed, so no approval of yours covers it"
	case e.AskKind == oversight.AskFew && e.Cover != nil:
		return fmt.Sprintf("few: you allowed steps that reach this far, but Limen runs one on its own only after approvals of weight %s from %s, and it has %s from %s",
			weight(e.Cover.MinWeight), plural(e.Cover.MinSessions, "session", "sessions"), weight(e.Cover.Weight), plural(e.Cover.Sessions, "session", "sessions"))
	case e.AskKind == oversight.AskFew:
		return "few: you allowed steps that reach this far, but not with enough considered answers or in enough sessions"
	}
	return string(e.AskKind)
}

// shortWhy is the short ask's words: the answers agree, and the chance is
// short of the threshold, as the console says it. Where the chance is
// hidden, they are Limen's own reason of a short ask, which states no
// chance.
func shortWhy(e *oversight.Explanation, hidden string) string {
	word := string(oversight.AskShort)
	switch {
	case hidden != "":
		return word + ": your answers about steps like this agree, but they are not enough yet to settle this step"
	case e.Thresholds == (oversight.Thresholds{}):
		return fmt.Sprintf("%s: not sure enough yet: Limen is %s sure you would allow it", word, percent(e.Approve))
	case e.Approve >= 0.5:
		return fmt.Sprintf("%s: not sure enough yet: Limen is %s sure you would allow it, and %s runs it on its own only above %s",
			word, percent(e.Approve), presetAt(e.Thresholds), percent(e.Thresholds.AllowAbove))
	}
	return fmt.Sprintf("%s: not sure enough yet: Limen is %s sure you would allow it, and %s stops it only below %s",
		word, percent(e.Approve), presetAt(e.Thresholds), percent(e.Thresholds.BlockBelow))
}

// weight is the weight of approvals as a person reads it: a whole number
// without a point, and otherwise to one place.
func weight(w float64) string {
	if w == math.Trunc(w) {
		return fmt.Sprintf("%.0f", w)
	}
	return fmt.Sprintf("%.1f", w)
}

// coverWords say how far a person's approvals cover a step: whether they
// do, the weight of the approvals that cover it against the weight
// needed, and their sessions against the sessions needed.
func coverWords(c *oversight.Cover) string {
	state := "not covered"
	if c.Covered {
		state = "covered"
	}
	return fmt.Sprintf("%s: approvals of weight %s of the %s needed, from %d of the %d sessions needed",
		state, weight(c.Weight), weight(c.MinWeight), c.Sessions, c.MinSessions)
}

// coverCell is a case's cover in one column: yes or no, the weight over
// the weight needed and the sessions over the sessions needed, or "-" for
// a prediction without one.
func coverCell(c *oversight.Cover) string {
	if c == nil {
		return "-"
	}
	state := "no"
	if c.Covered {
		state = "yes"
	}
	return fmt.Sprintf("%s %s/%s %d/%d", state, weight(c.Weight), weight(c.MinWeight), c.Sessions, c.MinSessions)
}

// presetAt is the name of the preset whose thresholds t are, as the
// console names the profile a step was decided under, and "your trust
// profile" for thresholds no preset gives, such as the 0.98 of a profile
// saved as Strict before Strict's costs changed. The thresholds compare
// to a half percent, which Balanced's rounded costs stay within.
func presetAt(t oversight.Thresholds) string {
	for _, p := range oversightPresets {
		run, stop := 1-p.costs.Ask/p.costs.FalseAllow, p.costs.Ask/p.costs.FalseBlock
		if math.Abs(run-t.AllowAbove) < 0.005 && math.Abs(stop-t.BlockBelow) < 0.005 {
			return p.label
		}
	}
	return "your trust profile"
}

// bothWays reports whether the answers that moved a prediction went both
// ways on one kind of step: an allow and a deny with the same label. A
// deny of another kind moves it through your answers lately and is no
// disagreement about this one. The explanation does not name the step's
// own label, and the answers of its own kind move it most, so one label
// with both counts.
func bothWays(evidence []oversight.Evidence) bool {
	allowed := map[string]bool{}
	for _, a := range evidence {
		if a.Approve {
			allowed[a.Label] = true
		}
	}
	for _, a := range evidence {
		if !a.Approve && allowed[a.Label] {
			return true
		}
	}
	return false
}

// learnedAsk is a kind's ask kind in the commands' words, Limen's own
// word, with one exception. A Limen that names no short, wider or few ask
// in the view calls an ask split by its evidence alone, so its split is
// short where the person did not both allow and deny steps of the kind.
func learnedAsk(k oversight.LearnedKind, knowsShort bool) string {
	if k.AskKind == oversight.AskSplit && !knowsShort && (k.Approved == 0 || k.Denied == 0) {
		return string(oversight.AskShort)
	}
	return string(k.AskKind)
}

// knowsShort reports whether a learned view comes from a Limen that names
// the asks of its cover, short, wider or few, and so splits only on
// answers that disagree.
func knowsShort(kinds []oversight.LearnedKind) bool {
	return slices.ContainsFunc(kinds, func(k oversight.LearnedKind) bool {
		return k.AskKind == oversight.AskShort || k.AskKind == oversight.AskWider || k.AskKind == oversight.AskFew
	})
}

// answerWord is an answer as a verb.
func answerWord(approve bool) string {
	if approve {
		return "allow"
	}
	return "deny"
}

// scale draws where p sits between a profile's thresholds on a line from 0
// to 1: stopped left of the first bar, asked between the bars, run on its
// own right of the second, with p as the star.
func scale(p float64, t oversight.Thresholds) string {
	return drawScale(&p, t)
}

// drawScale draws the line of scale, with no star where p is nil: the
// thresholds alone, for a chance that is not shown.
func drawScale(p *float64, t oversight.Thresholds) string {
	const width = 21
	at := func(v float64) int { return min(width-1, max(0, int(math.Round(v*(width-1))))) }
	line := []byte(strings.Repeat("-", width))
	line[at(t.BlockBelow)] = '|'
	line[at(t.AllowAbove)] = '|'
	if p != nil {
		line[at(*p)] = '*'
	}
	return "[" + string(line) + "]"
}

// ---- cases ----

func newOversightCasesCmd(apiURL, authURL *string) *cobra.Command {
	var (
		pending bool
		session string
		limit   int
		jsonF   bool
	)
	cmd := &cobra.Command{
		Use:   "cases",
		Short: "List the steps Limen holds about you, with what it suggested and what was done.",
		Long: `List the steps Limen holds about you, oldest first, the latest --limit of
them. Each shows the step's action id and kind, what Limen suggested (the
verdict, the chance you allow the step, how unsure it is, and for an ask
why it asks: unsure, split, short, wider or few), how far your approvals
cover the step (yes or no, their weight over the weight needed and their
sessions over the sessions needed), what was done (the verdict, the
chance you were shown the step, and what decided it), and your answers.

--pending lists only the steps you were shown that you have not checked
yet. --session keeps the steps of one session, in the order the session
made them, from each context its steps are in. Without --session, the
command lists your own context. --limit 0 lists them all. --json prints
the cases as the API answered them.`,
		Example: `  latere oversight cases
  latere oversight cases --pending
  latere oversight cases --session ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX --limit 0
  latere oversight cases --json --limit 5`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 0 {
				return refusedf("--limit is %d; give 0 for every case, or more", limit)
			}
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			session = strings.TrimSuffix(strings.TrimSpace(session), "/")
			shown, total, err := c.cases(cmd.Context(), cmd.ErrOrStderr(), pending, session, limit)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, raws(shown))
			}
			return printCases(out, shown, total, pending)
		},
	}
	cmd.Flags().BoolVar(&pending, "pending", false, "list only the steps waiting for you to check them")
	cmd.Flags().StringVar(&session, "session", "", "list only the steps of this session, by its id")
	cmd.Flags().IntVar(&limit, "limit", 20, "list the latest this many cases; 0 lists them all")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the cases as JSON")
	return cmd
}

// cases reads every page of the person's cases, and returns the latest
// limit of them, oldest first, with how many there were. Limen pages
// oldest first, so the latest are on the last page. Without a session they
// are the person's own context's. With one, they are that session's, read
// in each context its steps are in, and ordered as the session's log made
// them.
func (c *oversightClient) cases(ctx context.Context, errOut io.Writer, pending bool, session string, limit int) ([]*oversight.Case, int, error) {
	orgs := []string{c.me}
	var st sessionSteps
	if session != "" {
		st = c.steps(ctx, errOut, session)
		orgs = st.orgs(c.me)
	}
	all := []*oversight.Case{}
	for _, org := range orgs {
		params := &oversight.CasesParams{Org: org, Subject: c.me, Pending: pending, Limit: oversight.MaxCasesLimit}
		for k, err := range c.ov.ListCases(ctx, params) {
			if err != nil {
				return nil, 0, c.refusedIn(org, err)
			}
			if session != "" && !strings.HasPrefix(k.Action.ActionID, session+"/") {
				continue
			}
			all = append(all, k)
		}
	}
	if len(orgs) > 1 {
		// Each context pages oldest first; the session's log orders them
		// together, with a step it does not name last.
		at := make(map[string]int, len(st.order))
		for i, id := range st.order {
			at[session+"/"+id] = i
		}
		place := func(k *oversight.Case) int {
			if i, ok := at[k.Action.ActionID]; ok {
				return i
			}
			return len(st.order)
		}
		slices.SortStableFunc(all, func(a, b *oversight.Case) int { return cmp.Compare(place(a), place(b)) })
	}
	total := len(all)
	if limit > 0 && total > limit {
		all = all[total-limit:]
	}
	return all, total, nil
}

func printCases(out io.Writer, cases []*oversight.Case, total int, pending bool) error {
	if len(cases) == 0 {
		if pending {
			fprintln(out, "No steps wait for you to check them.")
		} else {
			fprintln(out, "Limen holds no steps of yours yet.")
		}
		return nil
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fprintln(w, "ACTION\tSTEP\tSUGGESTED\tALLOW\tUNCERTAINTY\tASK\tCOVER\tDECIDED\tSHOWN\tBY\tANSWERS")
	for _, k := range cases {
		suggested, allow, unsure, ask, cover := "-", "-", "-", "-", "-"
		if p := k.Prediction; p != nil {
			suggested, allow, unsure, ask, cover = string(p.Verdict), percent(p.Approve), fmt.Sprintf("%.2f", p.Uncertainty), dash(string(p.AskKind)), coverCell(p.Cover)
		}
		decided, shown, by := "-", "-", "-"
		if d := k.Decision; d != nil {
			decided, shown, by = string(d.Verdict), percent(d.ReviewProbability), dash(d.Source)
		}
		fprintf(w, "%s\t%s %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.Action.ActionID, k.Action.Kind, k.Action.Name,
			suggested, allow, unsure, ask, cover, decided, shown, by, dash(answersWords(k.Answers)))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fprint(out, stripANSI(b.String()))
	if len(cases) < total {
		fprintf(out, "\nThe latest %d of %d. --limit 0 lists them all.\n", len(cases), total)
	}
	if pending {
		fprintln(out, "\nCheck one with 'latere oversight answer <action> allow' if it was fine to run, or deny if Limen should have asked.")
	}
	return nil
}

// answersWords are a case's answers, oldest first: each an allow or a
// deny, and "check" for the one that checked a step the person was shown.
func answersWords(answers []oversight.RecordedAnswer) string {
	words := make([]string, len(answers))
	for i, a := range answers {
		words[i] = answerWord(a.Approve)
		if a.Review {
			words[i] += " (check)"
		}
	}
	return strings.Join(words, ", ")
}

// ---- explain ----

func newOversightExplainCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "explain <session>/<tool_use_id>",
		Short: "Say why Limen suggested what it did for one step.",
		Long: `Say why Limen suggested what it did for one step: the chance you allow it
and how unsure it is, how the chances Limen stated in the suggestion's
band compare with your checks, where the chance sits between your trust
profile's thresholds, how much of it comes from steps like this one, from your
answers lately, and from both together, how much your answers say about
it, and for an ask why: too few answers yet, answers that went both ways,
answers that agree with a chance short of the trust profile's threshold,
a step that reaches further than any you allowed, or approvals with too
little weight or from too few sessions. Then how far your approvals cover
the step, with the approvals nearest to it whose reach it exceeds, and
the answers of yours that moved it most, each with its share.

The model that explains is named with how many of your answers it was
fitted on; when it is not the one that made the suggestion, Limen has
learned since.

The chance is shown only where the suggestion's band is calibrated over
the last 90 days ('latere oversight calibration'), and above the run
threshold only while your trust profile's claim holds, which the command
then reads ('latere oversight reliability'). Otherwise no line states the
chance, and the command says why. --json prints the explanation as the
API answered it, the chance included.

The command reads the step's session to find the organization the step
was decided under, and reads your records there. A step that names none
is in your own context.`,
		Example: `  latere oversight explain ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01
  latere oversight explain ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseActionID(args[0])
			if err != nil {
				return err
			}
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			org := c.orgOfAction(ctx, cmd.ErrOrStderr(), id)
			e, err := c.ov.GetExplanation(ctx, &oversight.ExplanationParams{Org: org, Subject: c.me, ActionID: id})
			if err != nil {
				return c.refusedIn(org, err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), e.RawJSON())
			}
			hidden := chanceRule(e, func() (bool, error) {
				holds, err := c.certificateHolds(ctx, org)
				if err != nil {
					fprintf(cmd.ErrOrStderr(), "Could not read if your trust profile's claim holds, so the chance is not shown: %v\n", err)
				}
				return holds, err
			})
			return printExplanation(cmd.OutOrStdout(), e, c.appliedProfile(ctx, org, id), hidden)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the explanation as JSON")
	return cmd
}

// appliedProfile says whose trust profile applied to the step id names in
// the organization org, from the scope its recorded prediction names
// (Limen spec 010), which the explanation does not carry: "" in the
// person's own context, and where the case cannot be read or names none,
// since the explanation stands without it.
func (c *oversightClient) appliedProfile(ctx context.Context, org, id string) string {
	if !isOrganization(org) {
		return ""
	}
	page, err := c.ov.ListCasesPage(ctx, &oversight.CasesParams{Org: org, Subject: c.me, ActionID: id, Limit: 1})
	if err != nil || len(page.Items) == 0 || page.Items[0].Prediction == nil {
		return ""
	}
	return appliedWords(page.Items[0].Prediction.Profile)
}

// appliedWords say whose trust profile a prediction's scope names: the
// person's own stricter pick where it has a subject, which made the
// profile stricter for them; an agent's own where it has an agent; and
// otherwise the organization's.
func appliedWords(s oversight.Scope) string {
	switch {
	case s.Org == "":
		return ""
	case s.Subject != "":
		return "stricter, as you picked with 'latere oversight profile set --for-me'"
	case s.Agent != "":
		return "the agent's own in organization " + s.Org
	}
	return "organization " + s.Org + "'s"
}

// printExplanation prints an explanation. hidden is the words that stand
// in for its chance, as chanceRule gives them: where they are not "", no
// line states the chance, neither as a number nor as the star of the
// scale nor as the side of a threshold it is on.
func printExplanation(out io.Writer, e *oversight.Explanation, applied, hidden string) error {
	var b strings.Builder
	fprintf(&b, "%s\n\n", e.ActionID)
	chance := &e.Approve
	if hidden == "" {
		fprintf(&b, "Suggestion:   %s, %s likely you allow it (uncertainty %.2f)\n", e.Verdict, percent(e.Approve), e.Uncertainty)
	} else {
		chance = nil
		fprintf(&b, "Suggestion:   %s (uncertainty %.2f)\n", e.Verdict, e.Uncertainty)
		fprintf(&b, "Chance:       %s\n", hidden)
	}
	fprintf(&b, "Calibration:  %s\n", calibrationWords(e.Calibration))
	fprintf(&b, "Scale:        stop %s run  (stops below %s, runs above %s)\n", drawScale(chance, e.Thresholds), percent(e.Thresholds.BlockBelow), percent(e.Thresholds.AllowAbove))
	if applied != "" {
		fprintf(&b, "Profile:      %s\n", applied)
	}
	if e.AskKind != "" {
		fprintf(&b, "Why it asks:  %s\n", askWhy(e, hidden))
	}
	fprintf(&b, "Evidence:     your answers settled %s of what Limen did not know about this step (evidence ratio %.2f)\n", percent(e.EvidenceRatio), e.EvidenceRatio)
	if p := e.Parts; p != nil {
		fprintf(&b, "Parts:        steps like this %s, your answers lately %s, both together %s (sum %s)\n",
			signed(p.Action), signed(p.Recent), signed(p.Interaction), signed(e.Mean))
	}
	if c := e.Cover; c != nil {
		fprintf(&b, "Cover:        %s\n", coverWords(c))
		for i, n := range c.Nearest {
			lead := "              "
			if i == 0 {
				lead = "Nearest:      "
			}
			fprintf(&b, "%syou allowed %s, %s, %s\n", lead, dash(n.Label), formatTime(n.At), n.ActionID)
		}
	}
	fprintf(&b, "Model:        %s, fitted on %s\n", dash(e.Model.Source), plural(int(e.Model.Answers), "answer", "answers"))
	if p := e.PredictedBy; p != nil && *p != e.Model {
		fprintf(&b, "Suggested by: %s, fitted on %s; Limen has learned since\n", dash(p.Source), plural(int(p.Answers), "answer", "answers"))
	}
	if len(e.Evidence) == 0 {
		fprint(&b, "\nNo answers of yours moved it: Limen knows nothing about steps like this yet.\n")
		fprint(out, stripANSI(b.String()))
		return nil
	}
	fprint(&b, "\nThe answers that moved it most:\n")
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fprintln(w, "SHARE\tANSWER\tSTEP\tWHEN\tACTION")
	for _, ev := range e.Evidence {
		fprintf(w, "%s\t%s\t%s\t%s\t%s\n", percent(ev.Share), answerWord(ev.Approve), dash(ev.Label), formatTime(ev.At), ev.ActionID)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fprint(out, stripANSI(b.String()))
	return nil
}

// ---- learned ----

func newOversightLearnedCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "learned",
		Short: "List what Limen learned about you, per kind of step.",
		Long: `List what Limen learned about you: each kind of step you answered, how
many you allowed and denied, the chance you allow it now, how unsure Limen
is, where that sits between your trust profile's thresholds and what
Limen would suggest now. Then how much you let steps run lately, compared
with what Limen assumes of anyone.`,
		Example: `  latere oversight learned
  latere oversight learned --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			l, err := c.ov.GetLearned(cmd.Context(), &oversight.LearnedParams{Org: c.me, Subject: c.me})
			if err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), l.RawJSON())
			}
			return printLearned(cmd.OutOrStdout(), l)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print what Limen learned as JSON")
	return cmd
}

func printLearned(out io.Writer, l *oversight.Learned) error {
	if len(l.Kinds) == 0 {
		fprintln(out, "Limen has learned nothing about you yet: it learns from your Allow and Deny answers.")
		return nil
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fprintln(w, "STEP\tALLOWED\tDENIED\tALLOW\tUNCERTAINTY\tSCALE (stop | ask | run)\tNOW\tASK")
	knows := knowsShort(l.Kinds)
	for _, k := range l.Kinds {
		fprintf(w, "%s\t%d\t%d\t%s\t%.2f\t%s\t%s\t%s\n", dash(k.Label), k.Approved, k.Denied, percent(k.Approve), k.Uncertainty,
			scale(k.Approve, k.Thresholds), k.Verdict, dash(learnedAsk(k, knows)))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fprintf(&b, "\nModel: %s, fitted on %s.\n", dash(l.Model.Source), plural(int(l.Model.Answers), "answer", "answers"))
	if n := len(l.Recent); n > 0 {
		now := l.Recent[n-1]
		fprintf(&b, "Lately you let steps run %s (%s now", trendWords(now.Value), signed(now.Value))
		if n > 1 {
			fprintf(&b, ", %s at %s", signed(l.Recent[0].Value), formatTime(l.Recent[0].At))
		}
		fprint(&b, ").\n")
		values := make([]string, n)
		for i, p := range l.Recent {
			values[i] = signed(p.Value)
		}
		fprintf(&b, "Trend, oldest to now: %s\n", strings.Join(values, " "))
	}
	fprint(out, stripANSI(b.String()))
	return nil
}

// trendWords say how much a person lets steps run, from the shared
// tolerance part: above 0 more than Limen assumes of anyone, below less.
func trendWords(v float64) string {
	switch {
	case v > 0.15:
		return "more than usual"
	case v < -0.15:
		return "less than usual"
	}
	return "about as usual"
}

// ---- reliability ----

// reliabilityWindow is the window reliability reads when --from is not
// given.
const reliabilityWindow = 30 * 24 * time.Hour

func newOversightReliabilityCmd(apiURL, authURL *string) *cobra.Command {
	var (
		of, from, to string
		jsonF        bool
	)
	cmd := &cobra.Command{
		Use:   "reliability",
		Short: "Report how often Limen, or a rule set, went against you.",
		Long: `Report how often a source's verdicts went against you over a window: of the
steps it let run without asking, the share you would have denied, and of
those it stopped, the share you would have allowed, each estimated from
the steps you were shown and checked, with a 95% interval. Then whether
your trust profile's claim holds: how many checked steps in a row you
agreed with, and how many it needs.

--of names the source: learned, Limen's own suggestions (the default),
applied, the verdicts applied whatever decided them, or a rule set such
as rules/1. --from and --to bound the window, as a date such as
2026-10-01 or a time such as 2026-10-01T09:00:00Z; the window is the
last 30 days unless they say otherwise.`,
		Example: `  latere oversight reliability
  latere oversight reliability --of applied --from 2026-10-01
  latere oversight reliability --of rules/1 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now().UTC()
			end, err := parseWhen("--to", to, now)
			if err != nil {
				return err
			}
			start, err := parseWhen("--from", from, end.Add(-reliabilityWindow))
			if err != nil {
				return err
			}
			if !start.Before(end) {
				return refusedf("--from %s is not before --to %s", start.Format(time.RFC3339), end.Format(time.RFC3339))
			}
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			r, err := c.ov.GetReliability(cmd.Context(), &oversight.ReliabilityParams{Org: c.me, Subject: c.me, Of: of, From: start, To: end})
			if err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), r.RawJSON())
			}
			return printReliability(cmd.OutOrStdout(), r)
		},
	}
	cmd.Flags().StringVar(&of, "of", oversight.OfLearned, "the source: learned, applied, or a rule set such as rules/1")
	cmd.Flags().StringVar(&from, "from", "", "the window's start, a date or an RFC 3339 time (default 30 days before --to)")
	cmd.Flags().StringVar(&to, "to", "", "the window's end, a date or an RFC 3339 time (default now)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the report as JSON")
	return cmd
}

// parseWhen reads a flag's time: an RFC 3339 time, or a date, which is its
// start in UTC; empty is def.
func parseWhen(flag, s string, def time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Time{}, refusedf("%s %q is neither a date such as 2026-10-01 nor a time such as 2026-10-01T09:00:00Z", flag, s)
}

func printReliability(out io.Writer, r *oversight.Reliability) error {
	var b strings.Builder
	fprintf(&b, "How often %s went against you, %s to %s:\n\n", r.Of, formatTime(r.From), formatTime(r.To))
	fprintf(&b, "Ran without asking, you would have denied: %s\n", estimateWords(r.FalseAllow, "let run"))
	fprintf(&b, "Stopped, you would have allowed:           %s\n", estimateWords(r.FalseBlock, "stopped"))
	cert := r.Certificate
	holds := "not yet"
	if cert.Holds {
		holds = "it holds"
	}
	fprintf(&b, "\nYour trust profile claims under %s wrong at %s confidence: %s. %s in a row you agreed with, of %d needed.\n",
		percent(cert.Alpha), percent(1-cert.Delta), holds, plural(cert.Clean, "checked step", "checked steps"), cert.Needed)
	fprint(out, stripANSI(b.String()))
	return nil
}

// estimateWords say one error rate: its value and interval, what it rests
// on and how much it covers, or why it is unknown.
func estimateWords(e oversight.Estimate, did string) string {
	if e.Value == nil {
		s := "unknown, since nothing it " + did + " was shown to you"
		if e.Unanswered > 0 {
			s += fmt.Sprintf(" and checked; %d wait for you", e.Unanswered)
		}
		return s
	}
	s := percent(*e.Value)
	if e.Interval != nil {
		s += fmt.Sprintf(" (95%%: %s to %s)", percent(e.Interval[0]), percent(e.Interval[1]))
	}
	s += fmt.Sprintf(", from %s (%d picked at random), covering %s of what it %s", plural(e.Reviews, "check", "checks"), e.Audits, percent(e.Covered), did)
	if e.Unanswered > 0 {
		s += fmt.Sprintf("; %d shown and not checked yet", e.Unanswered)
	}
	return s
}

// ---- answer ----

func newOversightAnswerCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "answer <session>/<tool_use_id> allow|deny",
		Short: "Allow or deny a step, such as one you were shown to check.",
		Long: `Tell Limen whether you allow a step, or would have: allow for a step that
was fine to run ("Looks fine" in the console), deny for one Limen should
have asked about or stopped ("Should have asked"). Your first answer to a
step you were shown counts toward how often Limen is wrong; every answer
teaches it.

An answer is sent once and never again on its own, since each answer Limen
receives counts. It goes to the organization the step was decided under,
which the command reads from the step's session, or to your own context
for a step that names none.

The answer tells Limen that you gave it from a list of steps, such as
'latere oversight cases --pending', and nothing else about how you gave
it: the command does not see how long you looked at the step, whether
you opened its details, or what Limen showed you first.`,
		Example: `  latere oversight answer ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_01 allow
  latere oversight answer ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX/toolu_02 deny`,
		Args:      cobra.ExactArgs(2),
		ValidArgs: []string{"allow", "deny"},
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseActionID(args[0])
			if err != nil {
				return err
			}
			var approve bool
			switch strings.ToLower(args[1]) {
			case "allow":
				approve = true
			case "deny":
			default:
				return refusedf("%q is not an answer; give allow or deny", args[1])
			}
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			// The command measures nothing of how the person answered but
			// where, a step picked from a list of steps such as cases
			// --pending: a time, the details and the suggestion are left out.
			params := &oversight.AnswerParams{Org: c.orgOfAction(cmd.Context(), cmd.ErrOrStderr(), id), Subject: c.me, ActionID: id, Approve: approve,
				Surface: oversight.SurfaceQueue}
			if err := c.ov.Answer(cmd.Context(), params); err != nil {
				return c.refusedIn(params.Org, err)
			}
			if jsonF {
				// The SDK tags Remembered omitempty, which v2 does not apply to
				// false; the shallower field keeps it left out when unset.
				return printJSON(cmd.OutOrStdout(), struct {
					*oversight.AnswerParams
					Remembered bool `json:"remembered,omitzero"`
				}{params, params.Remembered})
			}
			fprintf(cmd.OutOrStdout(), "Answered %s for %s.\n", answerWord(approve), id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the answer sent as JSON")
	return cmd
}

// ---- forget ----

func newOversightForgetCmd(apiURL, authURL *string) *cobra.Command {
	var yes, jsonF bool
	cmd := &cobra.Command{
		Use:   "forget",
		Short: "Remove everything Limen holds about you.",
		Long: `Remove everything Limen holds about you: your steps, your answers, and
what it learned from them. Limen then knows nothing about you, and asks
about every step its rules leave open, as it does for anyone new. It
cannot be undone.

The command asks you to type yes unless --yes is given.`,
		Example: `  latere oversight forget
  latere oversight forget --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			if !yes {
				errOut := cmd.ErrOrStderr()
				fprint(errOut, "This removes everything Limen holds about you, your steps, your answers and what it learned, for good.\nType yes to forget everything: ")
				line, err := readLine(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read the answer: %w", err)
				}
				if !strings.EqualFold(line, "yes") {
					fprintln(errOut)
					return refusedf("nothing was removed: forgetting everything takes yes, typed in full. Pass --yes to forget without asking")
				}
			}
			params := &oversight.ForgetParams{Org: c.me, Subject: c.me}
			if err := c.ov.Forget(cmd.Context(), params); err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printJSON(cmd.OutOrStdout(), map[string]any{"org": c.me, "subject": c.me, "forgotten": true})
			}
			fprintln(cmd.OutOrStdout(), "Limen forgot everything it held about you.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "forget everything without asking")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print what was forgotten as JSON")
	return cmd
}
