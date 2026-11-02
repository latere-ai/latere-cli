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
  latere oversight profile                  your trust profile
  latere oversight profile set balanced     choose careful, balanced or strict
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
that names none. The other commands read your own context. Limen answers
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
		newOversightProfileCmd(&apiURL, &authURL),
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
	me, err := signedInSubject(ctx, origin, authURL, token)
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
	return &oversightClient{ov: oversight.New(cfg), base: base, me: me, authURL: authURL}, nil
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
// keys them: their issuer and their sign-in id with a | between them. It
// reads them from token when that is a JWT, and otherwise from the saved
// login, whose issuer the actor tokens minted from it share. An issuer the
// token does not name is the one the login resolves to.
func signedInSubject(ctx context.Context, origin, authURL, token string) (string, error) {
	raw, issuer := token, api.ResolveAuthURL(origin, authURL)
	if _, err := principalFromJWT(raw); raw == "" || err != nil {
		access, authBase, err := api.LoginToken(ctx, issuer)
		if err != nil {
			return "", refusedf("find who you are signed in as: %w", err)
		}
		raw, issuer = access, authBase
	}
	var claims struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
	}
	if err := jwt.DecodePayload(raw, &claims); err != nil || claims.Sub == "" {
		return "", refusedf("the saved login names no subject; run `latere login`")
	}
	return oversight.Subject(cmp.Or(claims.Iss, issuer), claims.Sub), nil
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

// askWords says what an ask kind means.
func askWords(k oversight.AskKind) string {
	switch k {
	case oversight.AskUnsure:
		return "unsure: your answers say little about steps like this yet, so more answers will move it"
	case oversight.AskSplit:
		return "split: your answers say a lot about steps like this, and they disagree, so more answers will not move it"
	}
	return string(k)
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
	const width = 21
	at := func(v float64) int { return min(width-1, max(0, int(math.Round(v*(width-1))))) }
	line := []byte(strings.Repeat("-", width))
	line[at(t.BlockBelow)] = '|'
	line[at(t.AllowAbove)] = '|'
	line[at(p)] = '*'
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
whether it was unsure or split), what was done (the verdict, the chance
you were shown the step, and what decided it), and your answers.

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
	fprintln(w, "ACTION\tSTEP\tSUGGESTED\tALLOW\tUNCERTAINTY\tASK\tDECIDED\tSHOWN\tBY\tANSWERS")
	for _, k := range cases {
		suggested, allow, unsure, ask := "-", "-", "-", "-"
		if p := k.Prediction; p != nil {
			suggested, allow, unsure, ask = string(p.Verdict), percent(p.Approve), fmt.Sprintf("%.2f", p.Uncertainty), dash(string(p.AskKind))
		}
		decided, shown, by := "-", "-", "-"
		if d := k.Decision; d != nil {
			decided, shown, by = string(d.Verdict), percent(d.ReviewProbability), dash(d.Source)
		}
		fprintf(w, "%s\t%s %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.Action.ActionID, k.Action.Kind, k.Action.Name,
			suggested, allow, unsure, ask, decided, shown, by, dash(answersWords(k.Answers)))
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
and how unsure it is, where that sits between your trust profile's
thresholds, how much of it comes from steps like this one, from your
answers lately, and from both together, how much your answers say about
it, and for an ask whether Limen is unsure or your answers are split. Then
the answers of yours that moved it most, each with its share.

The model that explains is named with how many of your answers it was
fitted on; when it is not the one that made the suggestion, Limen has
learned since.

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
			org := c.orgOfAction(cmd.Context(), cmd.ErrOrStderr(), id)
			e, err := c.ov.GetExplanation(cmd.Context(), &oversight.ExplanationParams{Org: org, Subject: c.me, ActionID: id})
			if err != nil {
				return c.refusedIn(org, err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), e.RawJSON())
			}
			return printExplanation(cmd.OutOrStdout(), e)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the explanation as JSON")
	return cmd
}

func printExplanation(out io.Writer, e *oversight.Explanation) error {
	var b strings.Builder
	fprintf(&b, "%s\n\n", e.ActionID)
	fprintf(&b, "Suggestion:   %s, %s likely you allow it (uncertainty %.2f)\n", e.Verdict, percent(e.Approve), e.Uncertainty)
	fprintf(&b, "Scale:        stop %s run  (stops below %s, runs above %s)\n", scale(e.Approve, e.Thresholds), percent(e.Thresholds.BlockBelow), percent(e.Thresholds.AllowAbove))
	if e.AskKind != "" {
		fprintf(&b, "Why it asks:  %s\n", askWords(e.AskKind))
	}
	fprintf(&b, "Evidence:     your answers settled %s of what Limen did not know about this step (evidence ratio %.2f)\n", percent(e.EvidenceRatio), e.EvidenceRatio)
	if p := e.Parts; p != nil {
		fprintf(&b, "Parts:        steps like this %s, your answers lately %s, both together %s (sum %s)\n",
			signed(p.Action), signed(p.Recent), signed(p.Interaction), signed(e.Mean))
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
	for _, k := range l.Kinds {
		fprintf(w, "%s\t%d\t%d\t%s\t%.2f\t%s\t%s\t%s\n", dash(k.Label), k.Approved, k.Denied, percent(k.Approve), k.Uncertainty,
			scale(k.Approve, k.Thresholds), k.Verdict, dash(string(k.AskKind)))
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

// ---- profile ----

// oversightAuditRate is the share of the steps run on their own that every
// preset shows the person to check: one in twenty, as the console's.
const oversightAuditRate = 0.05

// oversightPreset is a trust profile a person picks by name, as the
// console offers them.
type oversightPreset struct {
	name, label string
	costs       oversight.Costs
}

// oversightPresets are the console's trust profiles, Careful the one every
// person starts on.
var oversightPresets = []oversightPreset{
	{"careful", "Careful", oversight.Costs{FalseAllow: 10, FalseBlock: 4, Ask: 1}},
	{"balanced", "Balanced", oversight.Costs{FalseAllow: 2.857, FalseBlock: 2.857, Ask: 1}},
	{"strict", "Strict", oversight.Costs{FalseAllow: 50, FalseBlock: 4, Ask: 1}},
}

// presetOf is the preset whose costs c carries, comparing their ratios to
// a question's cost, or nil for costs no preset names.
func presetOf(c oversight.Costs) *oversightPreset {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	if c.Ask <= 0 {
		return nil
	}
	for i, p := range oversightPresets {
		if near(p.costs.FalseAllow/p.costs.Ask, c.FalseAllow/c.Ask) && near(p.costs.FalseBlock/p.costs.Ask, c.FalseBlock/c.Ask) {
			return &oversightPresets[i]
		}
	}
	return nil
}

func newOversightProfileCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Show your trust profile, or choose another with 'profile set'.",
		Long: `Show your trust profile: what a step that ran and that you would have
denied, a step that was stopped and that you would have allowed, and one
question cost, the thresholds that follow from them, and the share of the
steps that run on their own shown to you to check.

'latere oversight profile set' chooses one of the three profiles the
console offers: careful, the one everyone starts on, balanced, which asks
less, and strict, for money and production.`,
		Example: `  latere oversight profile
  latere oversight profile set balanced`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			p, err := c.ov.GetProfile(cmd.Context(), oversight.Scope{Org: c.me})
			if err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), p.RawJSON())
			}
			printProfile(cmd.OutOrStdout(), p)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the profile as JSON")
	cmd.AddCommand(newOversightProfileSetCmd(apiURL, authURL))
	return cmd
}

func newOversightProfileSetCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "set careful|balanced|strict",
		Short: "Choose your trust profile.",
		Long: `Choose your trust profile, one of the three the console offers:

  careful    runs a step on its own at 90% sure you would allow it, and
             stops it below 25%; the one everyone starts on
  balanced   asks less: runs a step at 65% sure, and stops it below 35%
  strict     for money and production: runs a step only at 98% sure

Each shows you 1 in 20 of the steps that run on their own to check.`,
		Example: `  latere oversight profile set balanced
  latere oversight profile set careful --json`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"careful", "balanced", "strict"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var preset *oversightPreset
			for i, p := range oversightPresets {
				if strings.EqualFold(args[0], p.name) {
					preset = &oversightPresets[i]
				}
			}
			if preset == nil {
				return refusedf("%q is not a trust profile; choose careful, balanced or strict", args[0])
			}
			c, err := newOversightClient(cmd.Context(), *apiURL, *authURL)
			if err != nil {
				return err
			}
			saved, err := c.ov.PutProfile(cmd.Context(), &oversight.Profile{Org: c.me, Costs: preset.costs, AuditRate: oversightAuditRate})
			if err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), saved.RawJSON())
			}
			printProfile(cmd.OutOrStdout(), saved)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the saved profile as JSON")
	return cmd
}

func printProfile(out io.Writer, p *oversight.Profile) {
	name := "Your own costs"
	if preset := presetOf(p.Costs); preset != nil {
		name = preset.label
	}
	if p.Default {
		name += " (you have not chosen one, so this is where everyone starts)"
	}
	var b strings.Builder
	fprintf(&b, "Trust profile: %s\n", name)
	fprintf(&b, "Runs a step on its own at %s sure you would allow it, asks between, and stops it below %s.\n",
		percent(p.Thresholds.AllowAbove), percent(p.Thresholds.BlockBelow))
	fprintf(&b, "Costs: a wrong run %g, a wrong stop %g, a question %g.\n", p.Costs.FalseAllow, p.Costs.FalseBlock, p.Costs.Ask)
	if p.AuditRate > 0 {
		fprintf(&b, "Shows you 1 in %.0f of the steps that run on their own to check.\n", 1/p.AuditRate)
	} else {
		fprint(&b, "Shows you none of the steps that run on their own to check.\n")
	}
	fprint(out, stripANSI(b.String()))
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
for a step that names none.`,
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
			params := &oversight.AnswerParams{Org: c.orgOfAction(cmd.Context(), cmd.ErrOrStderr(), id), Subject: c.me, ActionID: id, Approve: approve}
			if err := c.ov.Answer(cmd.Context(), params); err != nil {
				return c.refusedIn(params.Org, err)
			}
			if jsonF {
				return printJSON(cmd.OutOrStdout(), params)
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
