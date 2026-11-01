// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"latere.ai/x/sdk-go/agents"
)

func newAgentsStub(t *testing.T) *apiStub {
	t.Helper()
	return newAPIStub(t, agents.BasePath, envAgentsURL, envAgentsToken)
}

// agSession is a session as the agents API answers it, with a policy in
// mode when mode is not empty.
func agSession(id, title, mode string) string {
	s := `{"id":"` + id + `","agent":{"id":"agt_1","name":"helper","version":2},"title":"` + title + `","initiator":{"subject":"usr_42","kind":"person"},
"status":"idle","stop_reason":"end_turn","turn":3,"last_seq":40,"created_at":"2026-11-01T09:00:00Z","updated_at":"2026-11-01T09:10:00Z"`
	if mode != "" {
		s += `,"policy":{"mode":"` + mode + `","thresholds":{"flag_at":0.3,"ask_at":0.6,"block_at":0.9}}`
	}
	return s + `}`
}

// The sessions are listed newest first as the API pages them, up to
// --limit, with each one's approval mode.
func TestAgentsSessions(t *testing.T) {
	s := newAgentsStub(t)
	s.handle("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "p2" {
			_, _ = io.WriteString(w, `{"items":[`+agSession("ses_3", "third", "")+`]}`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[`+agSession("ses_1", "Fix the \\u001b[31mflaky\\u001b[0m test", "progressive")+`,`+agSession("ses_2", "", "confirm")+`],"next_cursor":"p2"}`)
	})
	out, errOut, code := runLatere(t, "", "agents", "sessions", "--limit", "2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "ID", "AGENT", "TITLE", "STATUS", "STARTED", "MODE", "ses_1", "helper", "Fix the flaky test", "idle (end_turn)", "2026-11-01 09:00 UTC", "progressive", "ses_2", "confirm")
	if strings.Contains(out, "ses_3") || strings.Contains(out, "\x1b") {
		t.Errorf("a session past the limit, or an escape, is printed:\n%q", out)
	}
	reqs := s.seen()
	if len(reqs) != 1 || reqs[0].URL.Query().Get("limit") != "2" || s.bearers[0] != "Bearer stub-token" {
		t.Errorf("%d requests, query %v, bearer %q", len(reqs), reqs[0].URL.Query(), s.bearers[0])
	}

	out, _, code = runLatere(t, "", "agents", "sessions", "--limit", "0", "--json")
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
		t.Fatalf("--json %q: %v", out, err)
	}
	if len(got) != 3 || got[0]["policy"].(map[string]any)["mode"] != "progressive" || got[2]["id"] != "ses_3" {
		t.Errorf("--json %v", got)
	}

	s.json("GET /sessions", http.StatusOK, `{"items":[]}`)
	out, _, _ = runLatere(t, "", "agents", "sessions")
	contains(t, out, "You have no hosted sessions.")
}

// agEvent is an event of the session's log.
func agEvent(seq int, typ, subagent, payload string) string {
	e := fmt.Sprintf(`{"id":"evt_%d","seq":%d,"session_id":"ses_1","type":%q,"time":"2026-11-01T09:%02d:00Z","payload":%s`, seq, seq, typ, seq, payload)
	if subagent != "" {
		e += `,"subagent":"` + subagent + `"`
	}
	return e + `}`
}

// A session's tool calls are read in order across the log's pages, each
// with how it was decided and the person's answer to it, and the footer
// names the commands that say why.
func TestAgentsSession(t *testing.T) {
	s := newAgentsStub(t)
	s.json("GET /sessions/ses_1", http.StatusOK, agSession("ses_1", "Release", "progressive"))
	push := `{"tool_use_id":"toolu_1","name":"bash","input":{"command":"git push\n origin main"},"verdict":"ask","reason":"listed","mode":"progressive",
"risk":{"score":0.6,"source":"rules/1"},"review_probability":1,"attended":true,
"suggestion":{"source":"learned/3","verdict":"ask","approve":0.58,"uncertainty":0.71,"thresholds":{"allow_above":0.9,"block_below":0.25},"audit_rate":0.05,"ask_kind":"split"},
"decided_by":"always_confirm","decided_by_rule":"bash(git push*)"}`
	read := `{"tool_use_id":"toolu_2","name":"read","input":{"file_path":"/work/README.md"},"verdict":"allow","mode":"progressive","review_probability":0.05,"attended":false,"decided_by":"read_only"}`
	old := `{"tool_use_id":"toolu_3","name":"web_fetch","input":{"url":"https://example.com"},"verdict":"allow"}`
	s.handle("GET /sessions/ses_1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "p2" {
			_, _ = io.WriteString(w, `{"items":[`+agEvent(4, "agent.tool_use", "sub_1", read)+`,`+agEvent(5, "agent.tool_use", "", old)+`]}`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[`+agEvent(1, "agent.message", "", `{"message":{"role":"assistant","blocks":[]}}`)+`,`+
			agEvent(2, "agent.tool_use", "", push)+`,`+
			agEvent(3, "user.tool_confirmation", "", `{"sender":{"subject":"usr_42","name":"Ada","kind":"person"},"tool_use_id":"toolu_1","decision":"allow","remember":"bash(git push*)"}`)+
			`],"next_cursor":"p2"}`)
	})
	out, errOut, code := runLatere(t, "", "agents", "session", "ses_1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "ses_1  helper  Release", "idle (end_turn), started 2026-11-01 09:00 UTC, mode progressive",
		"1. bash  git push origin main  [toolu_1]", "ask in progressive mode, decided by always_confirm: bash(git push*)",
		"risk 0.60 (rules/1), shown 100%, attended", "Limen suggested ask, 58% allow, uncertainty 0.71, split (learned/3)",
		"answered allow by Ada, remembering bash(git push*)",
		"2. read  /work/README.md  [toolu_2]  (subagent sub_1)", "allow in progressive mode, decided by read_only", "shown 5%, unattended",
		"3. web_fetch  https://example.com  [toolu_3]\n   allow\n\nWhy",
		"latere oversight explain ses_1/<tool_use_id>", "latere oversight cases --session ses_1")
	if strings.Count(out, "   answered") != 1 || strings.Count(out, "   Limen suggested") != 1 {
		t.Errorf("an answer or a suggestion is printed for a call that has none:\n%s", out)
	}
	if strings.Contains(out, "organization") {
		t.Errorf("a session of a person's agent names an organization:\n%s", out)
	}

	out, _, code = runLatere(t, "", "agents", "session", "ses_1", "--json")
	var got struct {
		Session   map[string]any `json:"session"`
		ToolCalls []struct {
			ActionID string         `json:"action_id"`
			Subagent string         `json:"subagent"`
			ToolUse  map[string]any `json:"tool_use"`
			Answer   map[string]any `json:"answer"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
		t.Fatalf("--json %q: %v", out, err)
	}
	if got.Session["id"] != "ses_1" || len(got.ToolCalls) != 3 || got.ToolCalls[0].ActionID != "ses_1/toolu_1" || got.ToolCalls[0].ToolUse["decided_by"] != "always_confirm" ||
		got.ToolCalls[0].Answer["decision"] != "allow" || got.ToolCalls[1].Subagent != "sub_1" || got.ToolCalls[2].Answer != nil {
		t.Errorf("--json %+v", got)
	}

	if _, errOut, code := runLatere(t, "", "agents", "session", "ses_9"); code != exitRefused || !strings.Contains(errOut, "not_found") {
		t.Errorf("an unknown session: exit %d, %s", code, errOut)
	}
}

// A session of an organization's agent names the organization, and a call
// Limen keeps in the organization's context names it too; a call keyed by
// the person, or by no one as an older one is, names none.
func TestAgentsSessionOfAnOrganizationsAgent(t *testing.T) {
	s := newAgentsStub(t)
	s.json("GET /sessions/ses_1", http.StatusOK, strings.Replace(agSession("ses_1", "Deploy", "progressive"), `"status":"idle"`, `"org":"org_7","status":"idle"`, 1))
	s.json("GET /sessions/ses_1/events", http.StatusOK, `{"items":[`+
		agEvent(1, "agent.tool_use", "", `{"tool_use_id":"toolu_1","name":"bash","input":{"command":"make deploy"},"verdict":"ask","decision_org":"org_7"}`)+`,`+
		agEvent(2, "agent.tool_use", "", `{"tool_use_id":"toolu_2","name":"bash","input":{"command":"ls"},"verdict":"allow","decision_org":"https://auth.example|usr_42"}`)+`,`+
		agEvent(3, "agent.tool_use", "", `{"tool_use_id":"toolu_3","name":"bash","input":{"command":"pwd"},"verdict":"allow"}`)+`]}`)
	out, errOut, code := runLatere(t, "", "agents", "session", "ses_1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "mode progressive, agent of organization org_7", "make deploy  [toolu_1]\n   ask\n   Limen keeps it in organization org_7\n")
	if strings.Count(out, "Limen keeps it") != 1 {
		t.Errorf("a call in the person's own context names an organization:\n%s", out)
	}
}
