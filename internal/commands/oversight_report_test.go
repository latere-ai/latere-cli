// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// ovReport is a report of three weeks: one with too few people, one ready
// and divided by agent, and one open.
const ovReport = `{"org":"org_7","from":"2026-09-28T00:00:00Z","to":"2026-10-19T00:00:00Z","min_subjects":5,"weeks":[
{"from":"2026-09-28T00:00:00Z","to":"2026-10-05T00:00:00Z","state":"too_few"},
{"from":"2026-10-05T00:00:00Z","to":"2026-10-12T00:00:00Z","state":"ready",
 "total":{"subjects":12,"decisions":{"allow":1610,"flag":92,"ask":212,"block":9},"flag_reviews":{"reviews":81,"denied":2},
  "applied":{"false_allow":{"value":0.021,"interval":[0.006,0.07],"covered":1,"reviews":81,"audits":81,"unanswered":11}},
  "learned":{"false_allow":{"value":null,"covered":0,"reviews":0,"audits":0,"unanswered":0}},
  "withheld":["applied.false_block","learned.false_block"]},
 "agents":[{"agent":"agent_01","subjects":9,"decisions":{"allow":1200,"flag":70,"ask":150,"block":6},"flag_reviews":{"reviews":63},"applied":{},"learned":{},
  "withheld":["flag_reviews.denied","applied.false_allow","applied.false_block","learned.false_allow","learned.false_block"]}],
 "other":{"subjects":7,"decisions":{"allow":410,"flag":22,"ask":62,"block":3},"flag_reviews":{"reviews":18},"applied":{},"learned":{},
  "withheld":["flag_reviews.denied","applied.false_allow","applied.false_block","learned.false_allow","learned.false_block"]}},
{"from":"2026-10-12T00:00:00Z","to":"2026-10-19T00:00:00Z","state":"open"}]}`

// An owner or an admin reads the organization's weeks: the ones too
// small to show, the ready ones with their numbers and the agents of the
// latest, a number withheld as not shown, and the open ones with when
// they show.
func TestOversightReport(t *testing.T) {
	ov, _ := newOrgOversightStubs(t)
	ov.json("GET /reports", http.StatusOK, ovReport)
	out, errOut, code := runLatere(t, "", "oversight", "report")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Organization org_7, the weeks from 2026-09-28 to 2026-10-19.",
		"2026-09-28", "too few people to show",
		"2026-10-05", "12", "1702", "212", "2 of 81 checks",
		"2026-10-12", "shows on 2026-10-26, so late checks count",
		"The week of 2026-10-05, by agent:", "agent_01", "1270", "not shown", "Other agents", "432",
		"Limen's suggestions:  not known yet", "The verdicts applied: 2% (95%: 1% to 7%), from 81 checks")
	// The default is the five weeks before this week's Monday.
	to := mondayOf(time.Now())
	q := ov.seen()[0].URL.Query()
	if q.Get("org") != ovOrg || q.Get("to") != to.Format(time.RFC3339) || q.Get("from") != to.AddDate(0, 0, -35).Format(time.RFC3339) {
		t.Errorf("query %v", q)
	}
	n := len(ov.seen())
	if _, errOut, code := runLatere(t, "", "oversight", "report", "--from", "2026-08-03", "--to", "2026-11-02"); code != 0 {
		t.Errorf("13 weeks: exit %d, %s", code, errOut)
	}
	if q := ov.seen()[n].URL.Query(); q.Get("from") != "2026-08-03T00:00:00Z" || q.Get("to") != "2026-11-02T00:00:00Z" {
		t.Errorf("query %v", q)
	}
	out, _, _ = runLatere(t, "", "oversight", "report", "--json")
	if !strings.Contains(out, `"min_subjects": 5`) {
		t.Errorf("--json %s", out)
	}

	// Bounds that are not Mondays, not in order, or more than 13 weeks
	// apart are refused before Limen is asked.
	n = len(ov.seen())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--from", "2026-10-07"}, "--from 2026-10-07T00:00:00Z is not the start of a week"},
		{[]string{"--to", "2026-10-05T09:00:00Z"}, "such as 2026-10-05"},
		{[]string{"--from", "2026-10-12", "--to", "2026-10-05"}, "is not before"},
		{[]string{"--from", "2026-07-27", "--to", "2026-11-02"}, "14 weeks apart; a report holds at most 13"},
		{[]string{"--from", "last week"}, "neither a date"},
	} {
		if _, errOut, code := runLatere(t, "", append([]string{"oversight", "report"}, c.args...)...); code != exitRefused || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, %s", c.args, code, errOut)
		}
	}
	if len(ov.seen()) != n {
		t.Error("refused bounds reached Limen")
	}

	// A member who is no owner or admin is refused with who reads it; an
	// outage may pass.
	ov.json("GET /reports", http.StatusForbidden, `{"error":{"code":"forbidden","message":"No.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "report"); code != exitRefused || !strings.Contains(errOut, "only an owner or an admin of organization org_7 can read its report") {
		t.Errorf("a member: exit %d, %s", code, errOut)
	}
	ov.json("GET /reports", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"Later.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "report"); code != exitNoAnswer {
		t.Errorf("an outage: exit %d, %s", code, errOut)
	}
}

// A report with no week of 5 or more people says so, and shows no
// numbers.
func TestOversightReportTooFew(t *testing.T) {
	ov, _ := newOrgOversightStubs(t)
	ov.json("GET /reports", http.StatusOK, `{"org":"org_7","from":"2026-09-28T00:00:00Z","to":"2026-10-12T00:00:00Z","min_subjects":5,"weeks":[
{"from":"2026-09-28T00:00:00Z","to":"2026-10-05T00:00:00Z","state":"too_few"},{"from":"2026-10-05T00:00:00Z","to":"2026-10-12T00:00:00Z","state":"too_few"}]}`)
	out, errOut, code := runLatere(t, "", "oversight", "report")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "No week has had 5 or more people yet")
	if strings.Contains(out, "by agent") || strings.Contains(out, "Limen's suggestions") {
		t.Errorf("a report without a ready week shows numbers: %s", out)
	}
}

func TestMondayOf(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-02T00:00:00Z":      "2026-11-02",
		"2026-11-08T23:59:59Z":      "2026-11-02",
		"2026-11-09T00:00:00Z":      "2026-11-09",
		"2026-11-04T10:00:00+09:00": "2026-11-02",
		"2026-11-02T01:00:00+02:00": "2026-10-26",
	} {
		at, err := time.Parse(time.RFC3339, in)
		if err != nil {
			t.Fatal(err)
		}
		if got := mondayOf(at); got.Format(time.DateOnly) != want || got.Location() != time.UTC || got.Hour() != 0 {
			t.Errorf("mondayOf(%s) = %s, want %s", in, got, want)
		}
	}
}
