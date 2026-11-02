// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"latere.ai/x/sdk-go/agents"
)

// session shows what each call reaches, says that a command's reach is not
// known where no one read it, shows each hand-back where it happened, and
// names a call the hand-back decided.
func TestAgentsSessionShowsTheReachAndTheHandBack(t *testing.T) {
	s := newAgentsStub(t)
	s.json("GET /sessions/ses_1", http.StatusOK, strings.Replace(agSession("ses_1", "Release", "progressive"), `"stop_reason":"end_turn"`, `"stop_reason":"denials"`, 1))
	push := `{"tool_use_id":"toolu_1","name":"bash","input":{"command":"git push --force origin main"},"verdict":"block","mode":"progressive","decided_by":"learned",
"scope":{"undo":"irreversible","crosses":{"machine":false,"remote":true,"public":false},"target_class":"primary","target_kind":"ref","source":"service"}}`
	bash := `{"tool_use_id":"toolu_2","name":"bash","input":{"command":"make deploy"},"verdict":"block","mode":"plan","decided_by":"plan","scope":{"source":"topos"}}`
	fetch := `{"tool_use_id":"toolu_3","name":"web_fetch","input":{"url":"https://example.com"},"verdict":"block","decided_by":"network",
"scope":{"undo":"none","crosses":{"remote":true,"network":true,"public":false},"target_kind":"host","contact":"first","source":"topos"}}`
	write := `{"tool_use_id":"toolu_4","name":"write","input":{"file_path":"/work/a.md"},"verdict":"ask","mode":"progressive","decided_by":"denials","review_probability":1,
"scope":{"undo":"reversible","crosses":{"machine":false,"remote":false},"target_class":"own","target_kind":"path","source":"topos"}}`
	old := `{"tool_use_id":"toolu_5","name":"read","input":{"file_path":"/work/b.md"},"verdict":"allow"}`
	s.json("GET /sessions/ses_1/events", http.StatusOK, `{"items":[`+
		agEvent(1, "agent.tool_use", "", push)+`,`+agEvent(2, "agent.tool_use", "", bash)+`,`+agEvent(3, "agent.tool_use", "", fetch)+`,`+
		agEvent(4, "session.denials_reached", "", `{"consecutive":3,"total":3,"limit":"consecutive","effect":"ask"}`)+`,`+
		agEvent(5, "agent.tool_use", "", write)+`,`+agEvent(6, "agent.tool_use", "", old)+`,`+
		agEvent(7, "session.denials_reached", "", `{"consecutive":1,"total":20,"limit":"total","effect":"stop"}`)+`]}`)
	out, errOut, code := runLatere(t, "", "agents", "session", "ses_1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out,
		"idle (denials)",
		"   reach: undo irreversible, crosses remote, does not cross machine or public, target primary ref (read by Limen)\n",
		"make deploy  [toolu_2]\n   block in plan mode, decided by plan\n   reach: not known: Limen did not read this call\n",
		"   reach: undo none, crosses remote and network, does not cross public, target host, first contact (the platform's claims)\n",
		"\n-- Handed back to you after 3 refused calls in a row (3 in this session): every call that changes something asks you until you write again.\n\n4. write",
		"ask in progressive mode, decided by denials (handed back to you after refused calls)",
		"   reach: undo reversible, does not cross machine or remote, target own path (the platform's claims)\n",
		"5. read  /work/b.md  [toolu_5]\n   allow\n\n-- Handed back to you after 20 refused calls in this session (1 in a row): the unattended turn stopped, and the next turn you begin asks you for every call that changes something until you write again.\n\nWhy")
	if strings.Count(out, "reach:") != 4 {
		t.Errorf("a call recorded before the platform wrote a scope shows a reach:\n%s", out)
	}

	out, _, code = runLatere(t, "", "agents", "session", "ses_1", "--json")
	var got struct {
		ToolCalls []map[string]any `json:"tool_calls"`
		HandBacks []struct {
			Seq     int64          `json:"seq"`
			Payload map[string]any `json:"payload"`
		} `json:"hand_backs"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
		t.Fatalf("--json %q: %v", out, err)
	}
	if len(got.ToolCalls) != 5 || len(got.HandBacks) != 2 || got.HandBacks[0].Seq != 4 || got.HandBacks[1].Payload["effect"] != "stop" {
		t.Errorf("--json %+v", got)
	}

	// A session whose only event is a hand-back still shows it.
	s.json("GET /sessions/ses_1/events", http.StatusOK, `{"items":[`+agEvent(1, "session.denials_reached", "", `{"consecutive":3,"total":3,"limit":"consecutive","effect":"other"}`)+`]}`)
	out, _, _ = runLatere(t, "", "agents", "session", "ses_1")
	contains(t, out, "\n-- Handed back to you after 3 refused calls in a row (3 in this session).\n")
}

// The reach of a call reads nothing for a call without a scope, says that
// it is not known for a scope with nothing known, and names a target by
// its class or its kind alone.
func TestReachWords(t *testing.T) {
	for _, tc := range []struct {
		scope *agents.CallScope
		want  string
	}{
		{nil, ""},
		{&agents.CallScope{Source: agents.ScopeByService}, "not known"},
		{&agents.CallScope{Source: agents.ScopeByPlatform, Crosses: &agents.Crosses{}}, "not known: Limen did not read this call"},
		{&agents.CallScope{Source: agents.ScopeByService, TargetKind: "package"}, "target package (read by Limen)"},
		{&agents.CallScope{Source: agents.ScopeByService, TargetClass: agents.TargetShared, Contact: agents.ContactRepeat}, "target shared, repeat contact (read by Limen)"},
	} {
		if got := reachWords(tc.scope); got != tc.want {
			t.Errorf("reachWords(%+v) = %q, want %q", tc.scope, got, tc.want)
		}
	}
	yes := true
	if got := reachWords(&agents.CallScope{Source: agents.ScopeByService, Crosses: &agents.Crosses{Machine: &yes, Remote: &yes, Public: &yes}}); got != "crosses machine, remote and public (read by Limen)" {
		t.Errorf("three boundaries: %q", got)
	}
}
