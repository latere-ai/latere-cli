// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/oversight"
)

// The report command reads an organization's weekly report for its owners
// and admins (platform spec 243, Limen spec 010): how its agents' steps
// went across everyone who used them, week by week. Limen shows a week 7
// days after it ends, and only when 5 or more people used the agents in
// it; it leaves out a number that rests mostly on one person's checks, and
// merges the agents fewer than 5 people used into the other agents. The
// command prints what Limen answers and works nothing out per person.

// reportWeeks is how many weeks report reads when --from is not given:
// the console's five.
const reportWeeks = 5

// week is the length of a report's week.
const week = 7 * 24 * time.Hour

// mondayOf is the Monday at 00:00 UTC that starts the week of t.
func mondayOf(t time.Time) time.Time {
	t = t.UTC()
	y, m, d := t.Date()
	return time.Date(y, m, d-(int(t.Weekday())+6)%7, 0, 0, 0, 0, time.UTC)
}

// reportBound reads a flag's bound of a report's weeks: a Monday at 00:00
// UTC, given as a date or a time; empty is def.
func reportBound(flag, s string, def time.Time) (time.Time, error) {
	t, err := parseWhen(flag, s, def)
	if err != nil {
		return time.Time{}, err
	}
	if monday := mondayOf(t); !t.Equal(monday) {
		return time.Time{}, refusedf("%s %s is not the start of a week: a report's weeks start on a Monday at 00:00 UTC, such as %s", flag, t.Format(time.RFC3339), monday.Format(time.DateOnly))
	}
	return t, nil
}

func newOversightReportCmd(apiURL, authURL *string) *cobra.Command {
	var (
		from, to string
		jsonF    bool
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Report how your organization's agents did, week by week, for its owners and admins.",
		Long: `Report how the agents of the organization your login acts in did, week by
week, across everyone who used them: how many steps ran on their own,
asked and were stopped, how many of the checked steps a check said should
have asked, and how often Limen's suggestions and the verdicts applied
ran a step that people would have stopped. Only the organization's owners
and admins can read it.

The report never shows one person. A week shows 7 days after it ends, so
that late checks count, and only if 5 or more people used the agents in
it; a smaller week says that it has too few people, and never how many. A
number that rests mostly on one person's checks is not shown. An agent
that fewer than 5 people used is counted in Other agents. A week that was
shown once never changes.

--from and --to bound the weeks: each is a Monday, as a date such as
2026-10-05 or a time at 00:00 UTC, and at most 13 weeks apart. The
default is the 5 weeks before the Monday that starts this week. --json
prints the report as the API answered it.`,
		Example: `  latere oversight report
  latere oversight report --from 2026-08-03 --to 2026-11-02
  latere oversight report --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now().UTC()
			end, err := reportBound("--to", to, mondayOf(now))
			if err != nil {
				return err
			}
			start, err := reportBound("--from", from, end.Add(-reportWeeks*week))
			if err != nil {
				return err
			}
			if !start.Before(end) {
				return refusedf("--from %s is not before --to %s", start.Format(time.DateOnly), end.Format(time.DateOnly))
			}
			if end.Sub(start) > oversight.ReportMaxWeeks*week {
				return refusedf("--from %s and --to %s are %d weeks apart; a report holds at most %d", start.Format(time.DateOnly), end.Format(time.DateOnly), int(end.Sub(start)/week), oversight.ReportMaxWeeks)
			}
			ctx := cmd.Context()
			c, err := newOversightClient(ctx, *apiURL, *authURL)
			if err != nil {
				return err
			}
			if c.org == "" {
				return refusedf("a report is of an organization, and your login acts in your personal context; your own numbers are in 'latere oversight reliability'. Sign in to the organization with `latere login --org-id <id>`")
			}
			r, err := c.ov.GetReport(ctx, &oversight.ReportParams{Org: c.org, From: start, To: end})
			if errors.Is(err, latere.ErrForbidden) {
				return refusedf("only an owner or an admin of organization %s can read its report; your own numbers are in 'latere oversight reliability'", c.org)
			}
			if err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), r.RawJSON())
			}
			return printReport(cmd.OutOrStdout(), r)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the first week's Monday, a date or an RFC 3339 time at 00:00 UTC (default 5 weeks before --to)")
	cmd.Flags().StringVar(&to, "to", "", "the Monday after the last week, a date or an RFC 3339 time at 00:00 UTC (default this week's Monday)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the report as JSON")
	return cmd
}

// ranOf counts a cell's steps that ran on their own: allowed, and run and
// then shown to a person to check.
func ranOf(c *oversight.ReportCell) int {
	return c.Decisions.Allow + c.Decisions.Flag
}

// withheld reports whether the cell leaves out the number at path.
func withheld(c *oversight.ReportCell, path string) bool {
	return slices.Contains(c.Withheld, path)
}

// checkedWords say what the checks of the flagged steps said: "2 of 81
// checks", or that the number is not shown.
func checkedWords(c *oversight.ReportCell) string {
	if c.FlagChecks.Denied == nil || withheld(c, "flag_reviews.denied") {
		return "not shown"
	}
	return fmt.Sprintf("%d of %d checks", *c.FlagChecks.Denied, c.FlagChecks.Checks)
}

// reportRow is one row of a report's table: a week or an agent, and its
// cell, or the words that stand for the numbers of a week that shows
// none.
func reportRow(w io.Writer, name string, c *oversight.ReportCell, none string) {
	if c == nil {
		fprintf(w, "%s\t-\t-\t-\t-\t%s\n", name, none)
		return
	}
	fprintf(w, "%s\t%d\t%d\t%d\t%d\t%s\n", name, c.Members, ranOf(c), c.Decisions.Ask, c.Decisions.Block, checkedWords(c))
}

// reportRateWords say one source's rate of a cell: of the steps that ran
// on their own, the share the people who checked them would have stopped,
// or why there is none.
func reportRateWords(e *oversight.Estimate, isWithheld bool) string {
	switch {
	case isWithheld:
		return "not shown: it rests mostly on one person's checks"
	case e == nil || e.Value == nil:
		return "not known yet: nothing it let run was checked"
	}
	s := percent(*e.Value)
	if e.Interval != nil {
		s += fmt.Sprintf(" (95%%: %s to %s)", percent(e.Interval[0]), percent(e.Interval[1]))
	}
	return s + ", from " + plural(e.Reviews, "check", "checks")
}

func printReport(out io.Writer, r *oversight.Report) error {
	var b strings.Builder
	fprintf(&b, "Organization %s, the weeks from %s to %s. The report counts everyone who used its agents and never shows one person.\n\n",
		r.Org, r.From.UTC().Format(time.DateOnly), r.To.UTC().Format(time.DateOnly))
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fprintln(w, "WEEK\tPEOPLE\tRAN ON ITS OWN\tASKED\tSTOPPED\tSHOULD HAVE ASKED")
	var latest *oversight.Week
	for i := range r.Weeks {
		wk := &r.Weeks[i]
		name := wk.From.UTC().Format(time.DateOnly)
		switch wk.State {
		case oversight.WeekReady:
			latest = wk
			reportRow(w, name, wk.Total, "")
		case oversight.WeekTooFew:
			reportRow(w, name, nil, "too few people to show")
		case oversight.WeekOpen:
			reportRow(w, name, nil, "shows on "+wk.To.Add(week).UTC().Format(time.DateOnly)+", so late checks count")
		default:
			reportRow(w, name, nil, string(wk.State))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if latest == nil || latest.Total == nil {
		fprint(&b, "\nNo week has had 5 or more people yet, so the report shows no numbers. Your own numbers are in 'latere oversight reliability'.\n")
		fprint(out, stripANSI(b.String()))
		return nil
	}
	day := latest.From.UTC().Format(time.DateOnly)
	if len(latest.Agents) > 0 || latest.Other != nil {
		fprintf(&b, "\nThe week of %s, by agent:\n", day)
		w = tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "AGENT\tPEOPLE\tRAN ON ITS OWN\tASKED\tSTOPPED\tSHOULD HAVE ASKED")
		for i := range latest.Agents {
			reportRow(w, latest.Agents[i].Agent, &latest.Agents[i].ReportCell, "")
		}
		if latest.Other != nil {
			reportRow(w, "Other agents", latest.Other, "")
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	t := latest.Total
	fprintf(&b, "\nThe week of %s: of the steps that ran on their own, the share the people who checked them would have stopped:\n", day)
	fprintf(&b, "  Limen's suggestions:  %s\n", reportRateWords(t.Learned.FalseAllow, withheld(t, "learned.false_allow")))
	fprintf(&b, "  The verdicts applied: %s\n", reportRateWords(t.Applied.FalseAllow, withheld(t, "applied.false_allow")))
	fprint(out, stripANSI(b.String()))
	return nil
}
