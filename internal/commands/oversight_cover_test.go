// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"net/http"
	"strings"
	"testing"
)

// coverExplanation is an explanation of an ask of askKind with a cover of
// weight and sessions, and the approvals nearest to the step.
func coverExplanation(askKind, cover string) string {
	return `{"org":"` + ovMe + `","subject":"` + ovMe + `","action_id":"ses_A/toolu_3","model":{"source":"learned/1","answers":4},
"approve":0.81,"uncertainty":0.5,"verdict":"ask","mean":0.9,"evidence_ratio":0.6,"ask_kind":"` + askKind + `","calibration":"calibrated","thresholds":{"allow_above":0.65,"block_below":0.35},
"evidence":[{"action_id":"ses_1/toolu_9","label":"publish: update","at":"2026-10-30T10:00:00Z","approve":true,"share":0.6},
{"action_id":"ses_1/toolu_8","label":"publish: make public","at":"2026-10-29T10:00:00Z","approve":false,"share":0.4}],"cover":` + cover + `}`
}

// explain says how far the person's approvals cover the step, names the
// approvals nearest to it, and words each ask of the cover; a split from a
// Limen with a cover is split, whatever the labels of its answers.
func TestOversightExplainSaysHowFarTheCoverIs(t *testing.T) {
	s := newOversightStub(t)
	// The chance, 81%, is above the run threshold of 65%, so the claim
	// must hold for it to show.
	s.json("GET /reliability", http.StatusOK, ovReliabilityHolds(true))
	s.json("GET /explanations", http.StatusOK, coverExplanation("wider", `{"covered":false,"weight":0,"sessions":0,"min_weight":3,"min_sessions":2,
"nearest":[{"action_id":"ses_1/toolu_9","label":"publish: update","at":"2026-10-30T10:00:00Z"},{"action_id":"ses_0/toolu_2","label":"publish: production","at":"2026-10-20T10:00:00Z"}]}`))
	out, errOut, code := runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out,
		"Why it asks:  wider: this step reaches further than any step of its kind that you allowed, so no approval of yours covers it\n",
		"Cover:        not covered: approvals of weight 0 of the 3 needed, from 0 of the 2 sessions needed\n",
		"Nearest:      you allowed publish: update, 2026-10-30 10:00 UTC, ses_1/toolu_9\n",
		"              you allowed publish: production, 2026-10-20 10:00 UTC, ses_0/toolu_2\n")

	s.json("GET /explanations", http.StatusOK, coverExplanation("few", `{"covered":false,"weight":2,"sessions":1,"min_weight":3,"min_sessions":2}`))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out,
		"Why it asks:  few: you allowed steps that reach this far, but Limen runs one on its own only after approvals of weight 3 from 2 sessions, and it has 2 from 1 session\n",
		"Cover:        not covered: approvals of weight 2 of the 3 needed, from 1 of the 2 sessions needed\n")
	if strings.Contains(out, "Nearest:") {
		t.Errorf("a cover with no nearest approval names one:\n%s", out)
	}

	s.json("GET /explanations", http.StatusOK, coverExplanation("short", `{"covered":true,"weight":3.5,"sessions":2,"min_weight":3,"min_sessions":2}`))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "Why it asks:  short: not sure enough yet: Limen is 81% sure you would allow it, and Balanced runs it on its own only above 65%\n",
		"Cover:        covered: approvals of weight 3.5 of the 3 needed, from 2 of the 2 sessions needed\n")

	// The answers of a split differ in label, and a Limen with a cover
	// splits only on answers that disagree.
	s.json("GET /explanations", http.StatusOK, coverExplanation("split", `{"covered":false,"weight":1,"sessions":1,"min_weight":3,"min_sessions":2}`))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "Why it asks:  split: your answers about steps like this went both ways")

	// A few ask without a cover says it in words.
	s.json("GET /explanations", http.StatusOK, strings.Replace(coverExplanation("few", `null`), `,"cover":null`, ``, 1))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "few: you allowed steps that reach this far, but not with enough considered answers or in enough sessions\n")
	if strings.Contains(out, "Cover:") {
		t.Errorf("an explanation without a cover shows one:\n%s", out)
	}
}

// cases shows each step's cover in a column of its own, and "-" for a
// prediction without one.
func TestOversightCasesShowTheCover(t *testing.T) {
	s := newOversightStub(t)
	withCover := func(id, ask, cover string) string {
		return strings.Replace(strings.Replace(ovCase(id, "ask", false), `"ask_kind":"split"`, `"ask_kind":"`+ask+`","cover":`+cover, 1), `,"reason":"r"`, ``, 1)
	}
	s.json("GET /cases", http.StatusOK, `{"entries":[`+
		withCover("ses_A/toolu_1", "wider", `{"covered":false,"weight":0,"sessions":0,"min_weight":3,"min_sessions":2}`)+`,`+
		withCover("ses_A/toolu_2", "few", `{"covered":false,"weight":1,"sessions":1,"min_weight":3,"min_sessions":2}`)+`,`+
		withCover("ses_A/toolu_3", "short", `{"covered":true,"weight":3,"sessions":2,"min_weight":3,"min_sessions":2}`)+`,`+
		ovCase("ses_A/toolu_4", "ask", false)+`]}`)
	out, errOut, code := runLatere(t, "", "oversight", "cases")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "ASK", "COVER")
	rows := map[string]string{}
	for line := range strings.Lines(out) {
		if id, _, ok := strings.Cut(line, " "); ok {
			rows[id] = line
		}
	}
	for id, want := range map[string][]string{
		"ses_A/toolu_1": {"wider", "no 0/3 0/2"},
		"ses_A/toolu_2": {"few", "no 1/3 1/2"},
		"ses_A/toolu_3": {"short", "yes 3/3 2/2"},
		"ses_A/toolu_4": {"split", "  -  "},
	} {
		for _, w := range want {
			if !strings.Contains(rows[id], w) {
				t.Errorf("%s: %q, want %q", id, rows[id], w)
			}
		}
	}
}

// learned shows Limen's words for a Limen that names the asks of its
// cover: a split of a kind the person only allowed stays split there.
func TestOversightLearnedFromALimenWithACover(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /learned", http.StatusOK, `{"org":"`+ovMe+`","subject":"`+ovMe+`","model":{"source":"learned/1","answers":5},"entries":[
{"label":"publish: update","kind":"tool","name":"publish","approved":2,"denied":0,"approve":0.8,"uncertainty":1.07,"verdict":"ask","evidence_ratio":0.6,"ask_kind":"short","thresholds":{"allow_above":0.9,"block_below":0.25}},
{"label":"publish: make public","kind":"tool","name":"publish","approved":1,"denied":0,"approve":0.6,"uncertainty":0.9,"verdict":"ask","evidence_ratio":0.6,"ask_kind":"split","thresholds":{"allow_above":0.9,"block_below":0.25}},
{"label":"bash: git push","kind":"tool","name":"bash","approved":1,"denied":0,"approve":0.9,"uncertainty":0.9,"verdict":"ask","evidence_ratio":0.6,"ask_kind":"wider","thresholds":{"allow_above":0.65,"block_below":0.35}}],"recent":[]}`)
	out, errOut, code := runLatere(t, "", "oversight", "learned")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "publish: update") && !strings.HasSuffix(line, "short"),
			strings.HasPrefix(line, "publish: make public") && !strings.HasSuffix(line, "split"),
			strings.HasPrefix(line, "bash: git push") && !strings.HasSuffix(line, "wider"):
			t.Errorf("row %q", line)
		}
	}
}

// answer tells Limen that the person answered a step picked from a list of
// steps, and sends
// nothing the command cannot measure.
func TestOversightAnswerSaysWhereThePersonAnswered(t *testing.T) {
	s := newOversightStub(t)
	s.handle("POST /answers", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	out, errOut, code := runLatere(t, "", "oversight", "answer", "ses_A/toolu_3", "allow", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	b := s.body(0)
	if b["surface"] != "queue" {
		t.Errorf("body %v", b)
	}
	for _, name := range []string{"answer_ms", "waited_ms", "details_opened", "suggestion_shown", "remembered"} {
		if _, ok := b[name]; ok {
			t.Errorf("sent %s, which the command cannot measure: %v", name, b)
		}
	}
	if !strings.Contains(out, `"surface": "queue"`) {
		t.Errorf("--json %s", out)
	}
}
