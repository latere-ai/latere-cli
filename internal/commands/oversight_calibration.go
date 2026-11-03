// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/oversight"
)

// The calibration command reads the person's calibration report (Limen
// spec 011, wave 2): for ten bands of the chance Limen stated, how often
// the person allowed the steps they checked. explain follows the report's
// rule for clients: it states the chance of a suggestion only where the
// suggestion's band is calibrated over the last 90 days, and above the run
// threshold only while the certificate of Limen's own suggestions holds.

// notProven is what explain says in place of a chance whose band Limen
// has not proven against the person's checks.
const notProven = "not shown: Limen's numbers in this band are not proven yet"

// The words explain says in place of a chance above the run threshold
// whose certificate does not hold, or cannot be read.
const (
	claimNotHeld = "not shown: above the run threshold, Limen's numbers count only while your trust profile's claim holds, and it does not hold yet"
	claimNotRead = "not shown: above the run threshold, Limen's numbers count only while your trust profile's claim holds, and the claim could not be read"
)

// calibrationWindowWords name the window of an explanation's calibration,
// oversight.CalibrationWindow.
const calibrationWindowWords = "the last 90 days"

// chanceRule applies the rule for clients of Limen spec 011 to an
// explanation. Its chance of allowing may be stated where the band of its
// recorded prediction is calibrated, and, for a chance above the run
// threshold, where the certificate of Limen's own suggestions also holds,
// which holds reads only then. chanceRule returns "" where the chance may
// be stated, and otherwise the words that stand in for it. An explanation
// with no calibration, from an older Limen or of a step with no recorded
// suggestion, states no chance.
func chanceRule(e *oversight.Explanation, holds func() (bool, error)) string {
	if e.Calibration != oversight.CalibrationCalibrated {
		return notProven
	}
	if e.Approve <= e.Thresholds.AllowAbove {
		return ""
	}
	ok, err := holds()
	switch {
	case err != nil:
		return claimNotRead
	case !ok:
		return claimNotHeld
	}
	return ""
}

// certificateHolds reads whether the certificate of Limen's own
// suggestions holds for the person in the context org, over the window of
// an explanation's calibration.
func (c *oversightClient) certificateHolds(ctx context.Context, org string) (bool, error) {
	now := time.Now().UTC()
	r, err := c.ov.GetReliability(ctx, &oversight.ReliabilityParams{Org: org, Subject: c.me, Of: oversight.OfLearned,
		From: now.Add(-oversight.CalibrationWindow), To: now})
	if err != nil {
		return false, err
	}
	return r.Certificate.Holds, nil
}

// calibrationWords say the state of a suggestion's band, as an
// explanation names it, with Limen's own word first.
func calibrationWords(s oversight.CalibrationState) string {
	switch s {
	case oversight.CalibrationCalibrated:
		return "calibrated: in this suggestion's band, the chances Limen stated agree with your checks of " + calibrationWindowWords
	case oversight.CalibrationOverconfident:
		return "overconfident: in this suggestion's band, Limen was surer than your checks of " + calibrationWindowWords + " show"
	case oversight.CalibrationUnderconfident:
		return "underconfident: in this suggestion's band, Limen was less sure than your checks of " + calibrationWindowWords + " show"
	case oversight.CalibrationNotEstimable:
		return "not_estimable: in this suggestion's band, you checked too few steps in " + calibrationWindowWords + " to tell"
	case "":
		return "not known: Limen sent none, as for a step with no recorded suggestion, or from an older Limen"
	}
	return string(s)
}

func newOversightCalibrationCmd(apiURL, authURL *string) *cobra.Command {
	var (
		from, to string
		jsonF    bool
	)
	cmd := &cobra.Command{
		Use:   "calibration",
		Short: "Compare the chances Limen stated for your steps with how often you allowed them.",
		Long: `Compare the chance that you allow a step, as Limen stated it, with how often
you allowed the steps you checked. The report has ten bands of the stated
chance, from 0% to 10% up to 90% to 100%. Each band shows how many steps
it holds, the share of them Limen could show you to check, the mean chance
Limen stated, the rate your checks measured with a 95% interval, your
checks, and how many of them were considered answers. Then its state:

  calibrated       the stated chance is in the interval
  overconfident    Limen was surer than your checks show
  underconfident   Limen was less sure than your checks show
  not_estimable    too few checks to tell; no rate is shown, which is not 0%

Under the bands: the error, the mean distance between the stated chance
and the measured rate over the bands with enough checks, and the share of
the steps in those bands.

'latere oversight explain' shows the chance of a suggestion only where its
band is calibrated over the last 90 days, and above the run threshold only
while your trust profile's claim holds ('latere oversight reliability').

--from and --to bound the window, as a date such as 2026-10-01 or a time
such as 2026-10-01T09:00:00Z; the window is the last 90 days unless they
say otherwise. --json prints the report as the API answered it.`,
		Example: `  latere oversight calibration
  latere oversight calibration --from 2026-10-01
  latere oversight calibration --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now().UTC()
			end, err := parseWhen("--to", to, now)
			if err != nil {
				return err
			}
			start, err := parseWhen("--from", from, end.Add(-oversight.CalibrationWindow))
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
			r, err := c.ov.GetCalibration(cmd.Context(), &oversight.CalibrationParams{Org: c.me, Subject: c.me, From: start, To: end})
			if err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), r.RawJSON())
			}
			return printCalibration(cmd.OutOrStdout(), r)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the window's start, a date or an RFC 3339 time (default 90 days before --to)")
	cmd.Flags().StringVar(&to, "to", "", "the window's end, a date or an RFC 3339 time (default now)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the report as JSON")
	return cmd
}

// chanceOrDash is a chance of a report, or "-" where the report has none,
// which is not 0%.
func chanceOrDash(p *float64) string {
	if p == nil {
		return "-"
	}
	return percent(*p)
}

// observedWords say a band's measured rate with its interval, or "-"
// where the band has none.
func observedWords(b oversight.CalibrationBin) string {
	if b.Observed == nil {
		return "-"
	}
	s := percent(*b.Observed)
	if b.Interval != nil {
		s += fmt.Sprintf(" (%s to %s)", percent(b.Interval[0]), percent(b.Interval[1]))
	}
	return s
}

func printCalibration(out io.Writer, r *oversight.Calibration) error {
	var b strings.Builder
	fprintf(&b, "The chances Limen stated for your steps, and how often you allowed the ones you checked, %s to %s:\n\n", formatTime(r.From), formatTime(r.To))
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fprintln(w, "BAND\tCASES\tCHECKABLE\tSTATED\tOBSERVED (95%)\tREVIEWS\tCONSIDERED\tSTATE")
	for _, bin := range r.Bins {
		fprintf(w, "%s to %s\t%d\t%s\t%s\t%s\t%d\t%d\t%s\n", percent(bin.From), percent(bin.To), bin.Cases, percent(bin.Covered),
			chanceOrDash(bin.Stated), observedWords(bin), bin.Reviews, bin.Considered, dash(string(bin.State)))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if r.Error == nil {
		fprint(&b, "\nError:      not known: no band has enough checks yet\n")
	} else {
		fprintf(&b, "\nError:      %.1f points, the mean distance between the stated chance and the measured rate over the bands with enough checks, each weighted by its cases\n", 100**r.Error)
	}
	fprintf(&b, "Estimable:  %s of the cases are in a band with enough checks\n", percent(r.Estimable))
	fprintf(&b, "\n'latere oversight explain' shows the chance of a suggestion only where its band is calibrated over %s, and above the run threshold only while your trust profile's claim holds.\n", calibrationWindowWords)
	fprint(out, stripANSI(b.String()))
	return nil
}
