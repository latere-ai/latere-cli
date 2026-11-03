// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"latere.ai/x/sdk-go/oversight"
)

// ovReliabilityHolds is a reliability report of Limen's own suggestions
// whose certificate holds, or does not.
func ovReliabilityHolds(holds bool) string {
	return fmt.Sprintf(`{"org":"`+ovMe+`","subject":"`+ovMe+`","of":"learned","from":"2026-08-05T00:00:00Z","to":"2026-11-03T00:00:00Z",
"false_allow":{"value":0,"interval":[0,0.01],"covered":0.05,"reviews":300,"audits":300,"unanswered":0},
"false_block":{"value":null,"covered":0,"reviews":0,"audits":0,"unanswered":0},
"certificate":{"alpha":0.01,"delta":0.05,"clean":300,"needed":299,"holds":%t}}`, holds)
}

// ovCalibration is a calibration report with an empty band, a band with
// too few checks, a calibrated band, an overconfident one and an
// underconfident one, the others empty, and the report's error.
func ovCalibration(errorValue string) string {
	bins := make([]string, oversight.CalibrationBinCount)
	for i := range bins {
		bins[i] = fmt.Sprintf(`{"from":%g,"to":%g,"cases":0,"covered":0,"stated":null,"observed":null,"reviews":0,"considered":0,"state":"not_estimable"}`, float64(i)/10, float64(i+1)/10)
	}
	bins[2] = `{"from":0.2,"to":0.3,"cases":6,"covered":1,"stated":0.24,"observed":null,"reviews":6,"considered":4,"state":"not_estimable"}`
	bins[5] = `{"from":0.5,"to":0.6,"cases":40,"covered":1,"stated":0.55,"observed":0.6,"interval":[0.45,0.73],"reviews":40,"considered":31,"state":"calibrated"}`
	bins[8] = `{"from":0.8,"to":0.9,"cases":90,"covered":0.5,"stated":0.86,"observed":0.62,"interval":[0.5,0.72],"reviews":45,"considered":20,"state":"overconfident"}`
	bins[9] = `{"from":0.9,"to":1,"cases":400,"covered":0.05,"stated":0.93,"observed":0.99,"interval":[0.95,1],"reviews":20,"considered":12,"state":"underconfident"}`
	return `{"org":"` + ovMe + `","subject":"` + ovMe + `","from":"2026-08-05T00:00:00Z","to":"2026-11-03T00:00:00Z","bins":[` +
		strings.Join(bins, ",") + `],"error":` + errorValue + `,"estimable":0.98}`
}

// lineStarting is the line of out that starts with prefix, or "".
func lineStarting(out, prefix string) string {
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// The report prints the ten bands with what each stated and measured, a
// band with too few checks as not_estimable with no rate, which is not
// 0%, and the error and the estimable share under them.
func TestOversightCalibration(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /calibration", http.StatusOK, ovCalibration("0.064"))
	out, errOut, code := runLatere(t, "", "oversight", "calibration", "--from", "2026-08-05", "--to", "2026-11-03T00:00:00Z")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "2026-08-05 00:00 UTC to 2026-11-03 00:00 UTC",
		"BAND", "CASES", "CHECKABLE", "STATED", "OBSERVED (95%)", "REVIEWS", "CONSIDERED", "STATE",
		"Error:      6.4 points, the mean distance between the stated chance and the measured rate",
		"Estimable:  98% of the cases are in a band with enough checks",
		"'latere oversight explain' shows the chance of a suggestion only where its band is calibrated over the last 90 days")
	for prefix, want := range map[string][]string{
		"0% to 10% ":   {"0", "-", "not_estimable"},
		"20% to 30% ":  {"6", "100%", "24%", "-", "4", "not_estimable"},
		"50% to 60% ":  {"40", "100%", "55%", "60% (45% to 73%)", "31", "calibrated"},
		"80% to 90% ":  {"90", "50%", "86%", "62% (50% to 72%)", "45", "20", "overconfident"},
		"90% to 100% ": {"400", "5%", "93%", "99% (95% to 100%)", "20", "12", "underconfident"},
	} {
		line := lineStarting(out, prefix)
		if line == "" {
			t.Errorf("no band %q:\n%s", prefix, out)
			continue
		}
		contains(t, line, want...)
	}
	// A band with too few checks has no rate, never 0%.
	if line := lineStarting(out, "20% to 30% "); strings.Count(line, " 0%") > 0 {
		t.Errorf("a band with no rate shows 0%%: %q", line)
	}
	bands := 0
	for line := range strings.Lines(out) {
		if band, _, ok := strings.Cut(line, "  "); ok && strings.HasSuffix(band, "%") && strings.Contains(band, "% to ") {
			bands++
		}
	}
	if bands != oversight.CalibrationBinCount {
		t.Errorf("%d bands, want %d:\n%s", bands, oversight.CalibrationBinCount, out)
	}
	q := s.seen()[0].URL.Query()
	if s.seen()[0].URL.Path != oversight.BasePath+"/calibration" || q.Get("org") != ovMe || q.Get("subject") != ovMe ||
		q.Get("from") != "2026-08-05T00:00:00Z" || q.Get("to") != "2026-11-03T00:00:00Z" {
		t.Errorf("request %v", s.seen()[0].URL)
	}

	// The window defaults to the 90 days before now, the window of an
	// explanation's calibration.
	if _, errOut, code := runLatere(t, "", "oversight", "calibration"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	q = s.seen()[1].URL.Query()
	from, _ := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, _ := time.Parse(time.RFC3339Nano, q.Get("to"))
	if to.Sub(from) != oversight.CalibrationWindow || time.Since(to) > time.Minute {
		t.Errorf("default query %v", q)
	}

	out, _, _ = runLatere(t, "", "oversight", "calibration", "--json")
	if !strings.Contains(out, `"state": "not_estimable"`) || !strings.Contains(out, `"observed": null`) {
		t.Errorf("--json %s", out)
	}

	// A bad window is refused before Limen is asked; a refusal exits 2 and
	// an outage 3.
	n := len(s.seen())
	if _, errOut, code := runLatere(t, "", "oversight", "calibration", "--from", "last week"); code != exitRefused || !strings.Contains(errOut, "--from") {
		t.Errorf("a bad --from: exit %d, %s", code, errOut)
	}
	if _, errOut, code := runLatere(t, "", "oversight", "calibration", "--from", "2026-11-02", "--to", "2026-11-01"); code != exitRefused || !strings.Contains(errOut, "not before") {
		t.Errorf("a window backwards: exit %d, %s", code, errOut)
	}
	if len(s.seen()) != n {
		t.Error("a bad window reached Limen")
	}
	s.json("GET /calibration", http.StatusForbidden, `{"error":{"code":"forbidden","message":"No.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "calibration"); code != exitRefused {
		t.Errorf("a refusal: exit %d, %s", code, errOut)
	}
	s.json("GET /calibration", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"Later.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "calibration"); code != exitNoAnswer {
		t.Errorf("an outage: exit %d, %s", code, errOut)
	}
}

// A report with no band of enough checks has no error, which is not zero.
func TestOversightCalibrationWithNoEstimableBand(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /calibration", http.StatusOK, strings.Replace(ovCalibration("null"), `"estimable":0.98`, `"estimable":0`, 1))
	out, errOut, code := runLatere(t, "", "oversight", "calibration")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Error:      not known: no band has enough checks yet\n", "Estimable:  0% of the cases")
}

// calibratedExplanation is an explanation of an ask with the chance
// approve, a run threshold of 90%, and the calibration state, none where
// state is empty.
func calibratedExplanation(approve, state string) string {
	calibration := ""
	if state != "" {
		calibration = `"calibration":"` + state + `",`
	}
	return `{"org":"` + ovMe + `","subject":"` + ovMe + `","action_id":"ses_A/toolu_3","model":{"source":"learned/4","answers":12},
"approve":` + approve + `,"uncertainty":0.5,"verdict":"ask","mean":1.4,"evidence_ratio":0.7,"ask_kind":"short",` + calibration + `"thresholds":{"allow_above":0.9,"block_below":0.25},
"evidence":[{"action_id":"ses_1/toolu_9","label":"publish","at":"2026-10-30T10:00:00Z","approve":true,"share":0.6}]}`
}

// noChance fails the test where a line of out states the chance pct: as
// a number, as "likely" or "sure you would", or as the star of the scale.
func noChance(t *testing.T, out, pct string) {
	t.Helper()
	if strings.Contains(out, pct) || strings.Contains(out, "likely") || strings.Contains(out, "sure you would") || strings.Contains(lineStarting(out, "Scale:"), "*") {
		t.Errorf("the chance %s is stated:\n%s", pct, out)
	}
}

// explain states the chance only for a calibrated band, and above the run
// threshold only while the claim of Limen's own suggestions holds; in
// every other case, no line states it, and the command says why.
func TestOversightExplainFollowsTheRuleForClients(t *testing.T) {
	s := newOversightStub(t)

	// A calibrated band below the run threshold: the chance shows, and
	// the claim is not read.
	s.json("GET /explanations", http.StatusOK, calibratedExplanation("0.83", "calibrated"))
	out, errOut, code := runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Suggestion:   ask, 83% likely you allow it (uncertainty 0.50)\n",
		"Calibration:  calibrated: in this suggestion's band, the chances Limen stated agree with your checks of the last 90 days\n",
		"Why it asks:  short: not sure enough yet: Limen is 83% sure you would allow it, and Careful runs it on its own only above 90%\n")
	if !strings.Contains(lineStarting(out, "Scale:"), "*") || strings.Contains(out, "Chance:") {
		t.Errorf("a calibrated chance is hidden:\n%s", out)
	}
	if len(s.seen()) != 1 {
		t.Errorf("%d requests; a chance below the run threshold reads no claim", len(s.seen()))
	}

	// Any other state, or none, hides the chance, with no claim read.
	for state, words := range map[string]string{
		"overconfident":  "overconfident: in this suggestion's band, Limen was surer than your checks of the last 90 days show",
		"underconfident": "underconfident: in this suggestion's band, Limen was less sure than your checks of the last 90 days show",
		"not_estimable":  "not_estimable: in this suggestion's band, you checked too few steps in the last 90 days to tell",
		"":               "not known: Limen sent none, as for a step with no recorded suggestion, or from an older Limen",
	} {
		n := len(s.seen())
		s.json("GET /explanations", http.StatusOK, calibratedExplanation("0.83", state))
		out, errOut, code := runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
		if code != 0 {
			t.Fatalf("%q: exit %d: %s", state, code, errOut)
		}
		contains(t, out, "Suggestion:   ask (uncertainty 0.50)\n", "Chance:       not shown: Limen's numbers in this band are not proven yet\n",
			"Calibration:  "+words+"\n",
			"Scale:        stop [-----|------------|--] run  (stops below 25%, runs above 90%)\n",
			"Why it asks:  short: your answers about steps like this agree, but they are not enough yet to settle this step\n")
		noChance(t, out, "83%")
		if len(s.seen()) != n+1 {
			t.Errorf("%q: %d requests; a band that is not calibrated reads no claim", state, len(s.seen())-n)
		}
	}

	// A calibrated band above the run threshold: the chance shows while
	// the claim of Limen's own suggestions over the last 90 days holds.
	s.json("GET /explanations", http.StatusOK, calibratedExplanation("0.95", "calibrated"))
	s.json("GET /reliability", http.StatusOK, ovReliabilityHolds(true))
	n := len(s.seen())
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "Suggestion:   ask, 95% likely you allow it (uncertainty 0.50)\n")
	reqs := s.seen()[n:]
	if len(reqs) != 2 || reqs[1].URL.Path != oversight.BasePath+"/reliability" {
		t.Fatalf("requests %v", reqs)
	}
	q := reqs[1].URL.Query()
	from, _ := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, _ := time.Parse(time.RFC3339Nano, q.Get("to"))
	if q.Get("of") != oversight.OfLearned || q.Get("org") != ovMe || q.Get("subject") != ovMe || to.Sub(from) != oversight.CalibrationWindow || time.Since(to) > time.Minute {
		t.Errorf("the claim's query %v", q)
	}

	// A claim that does not hold hides it.
	s.json("GET /reliability", http.StatusOK, ovReliabilityHolds(false))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "Chance:       not shown: above the run threshold, Limen's numbers count only while your trust profile's claim holds, and it does not hold yet\n",
		"Calibration:  calibrated:")
	noChance(t, out, "95%")

	// A claim that cannot be read hides it too, with a note.
	s.json("GET /reliability", http.StatusForbidden, `{"error":{"code":"forbidden","message":"No.","details":{}}}`)
	out, errOut, code = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	if code != 0 {
		t.Fatalf("a claim that cannot be read fails explain: exit %d, %s", code, errOut)
	}
	contains(t, out, "Chance:       not shown: above the run threshold, Limen's numbers count only while your trust profile's claim holds, and the claim could not be read\n")
	contains(t, errOut, "Could not read if your trust profile's claim holds, so the chance is not shown")
	noChance(t, out, "95%")

	// --json prints the explanation as the API answered it.
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3", "--json")
	if !strings.Contains(out, `"approve": 0.95`) || !strings.Contains(out, `"calibration": "calibrated"`) {
		t.Errorf("--json %s", out)
	}
}

func TestChanceRule(t *testing.T) {
	never := func() (bool, error) {
		t.Error("the claim is read")
		return false, nil
	}
	at := func(approve float64, state oversight.CalibrationState) *oversight.Explanation {
		return &oversight.Explanation{Approve: approve, Calibration: state, Thresholds: oversight.Thresholds{AllowAbove: 0.9, BlockBelow: 0.25}}
	}
	if got := chanceRule(at(0.9, oversight.CalibrationCalibrated), never); got != "" {
		t.Errorf("a chance at the run threshold is not above it: %q", got)
	}
	if got := chanceRule(at(0.1, oversight.CalibrationCalibrated), never); got != "" {
		t.Errorf("a calibrated chance below the stop threshold: %q", got)
	}
	if got := chanceRule(at(0.5, "drifting"), never); got != notProven {
		t.Errorf("a state the command does not know: %q", got)
	}
	if got := chanceRule(&oversight.Explanation{Approve: 0.5, Calibration: oversight.CalibrationCalibrated}, func() (bool, error) { return true, nil }); got != "" {
		t.Errorf("no thresholds, with the claim holding: %q", got)
	}
	if got := calibrationWords("drifting"); got != "drifting" {
		t.Errorf("an unknown state reads %q", got)
	}
}
