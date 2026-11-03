// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// newOrgOversightStubs is the oversight API and the agents API, with a
// person signed in to the organization ovOrg.
func newOrgOversightStubs(t *testing.T) (ov, ag *apiStub) {
	t.Helper()
	ov, ag = newOversightStubs(t)
	saveLogin(t, map[string]any{"iss": "https://auth.example", "sub": "usr_42", "org_id": ovOrg})
	return ov, ag
}

// ovAgent is the agent release-notes as the agents API answers it: Limen
// keys its steps by its id.
const ovAgent = `{"apiVersion":"topos.latere.ai/v1","kind":"Agent","metadata":{"name":"release-notes","displayName":"Release notes"},
"spec":{},"status":{"id":"agent_01","version":2}}`

// ovProfile is a profile of scope with the costs of a preset, as Limen
// answers it.
func ovProfile(scope, preset string) string {
	costs := map[string]string{
		"careful":  `"costs":{"false_allow":10,"false_block":4,"ask":1},"thresholds":{"allow_above":0.9,"block_below":0.25}`,
		"balanced": `"costs":{"false_allow":2.857,"false_block":2.857,"ask":1},"thresholds":{"allow_above":0.65,"block_below":0.35}`,
		"strict":   `"costs":{"false_allow":20,"false_block":4,"ask":1},"thresholds":{"allow_above":0.95,"block_below":0.25}`,
		// strict-50 is a profile saved as Strict before Strict's costs
		// became 20, 4, 1: no preset names its costs any more.
		"strict-50": `"costs":{"false_allow":50,"false_block":4,"ask":1},"thresholds":{"allow_above":0.98,"block_below":0.25}`,
		// own is costs no preset names, that run a step at 80% and stop
		// it below 10%.
		"own": `"costs":{"false_allow":5,"false_block":10,"ask":1},"thresholds":{"allow_above":0.8,"block_below":0.1}`,
	}[preset]
	return `{` + scope + `,` + costs + `,"audit_rate":0.05}`
}

// An owner or an admin reads, sets and removes an agent's own profile in
// the organization the login acts in, by the agent's name, which the
// agents API turns into the id Limen keys the agent by.
func TestOversightAgentProfile(t *testing.T) {
	ov, ag := newOrgOversightStubs(t)
	ag.json("GET /agents/release-notes", http.StatusOK, ovAgent)
	own, ownPreset := true, "balanced"
	ov.handle("GET /profiles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if own {
			_, _ = io.WriteString(w, ovProfile(`"org":"`+ovOrg+`","agent":"agent_01"`, ownPreset))
			return
		}
		_, _ = io.WriteString(w, ovProfile(`"org":"`+ovOrg+`"`, "careful"))
	})
	out, errOut, code := runLatere(t, "", "oversight", "profile", "--agent", "release-notes")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Release notes has its own trust profile in organization org_7, set by an owner or an admin.", "Trust profile: Balanced\n")
	if q := ov.seen()[0].URL.Query(); q.Get("org") != ovOrg || q.Get("agent") != "agent_01" || q.Has("subject") {
		t.Errorf("query %v", q)
	}
	own = false
	out, _, _ = runLatere(t, "", "oversight", "profile", "--agent", "release-notes")
	contains(t, out, "Release notes follows organization org_7's trust profile.", "Trust profile: Careful\n")
	// An agent's own profile saved as Strict before Strict's costs changed
	// reads by its own thresholds and names no preset.
	own, ownPreset = true, "strict-50"
	out, _, _ = runLatere(t, "", "oversight", "profile", "--agent", "release-notes")
	contains(t, out, "Release notes has its own trust profile in organization org_7", "Trust profile: Its own costs\n", "at 98% sure", "below 25%")
	if strings.Contains(out, "Strict") {
		t.Errorf("an earlier Strict reads as Strict: %s", out)
	}
	own = false

	ov.json("PUT /profiles", http.StatusOK, ovProfile(`"org":"`+ovOrg+`","agent":"agent_01"`, "strict"))
	n := len(ov.seen())
	out, errOut, code = runLatere(t, "", "oversight", "profile", "set", "strict", "--agent", "release-notes")
	if code != 0 {
		t.Fatalf("set: exit %d: %s", code, errOut)
	}
	contains(t, out, "Release notes now has its own trust profile in organization org_7: Strict, for everyone's sessions with it from its next step.")
	if b := ov.body(n); b["org"] != ovOrg || b["agent"] != "agent_01" || b["subject"] != nil || b["audit_rate"] != 0.05 || b["costs"].(map[string]any)["false_allow"] != 20.0 {
		t.Errorf("saved %v", b)
	}

	ov.handle("DELETE /profiles", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	n = len(ov.seen())
	out, errOut, code = runLatere(t, "", "oversight", "profile", "reset", "--agent", "release-notes")
	if code != 0 {
		t.Fatalf("reset: exit %d: %s", code, errOut)
	}
	contains(t, out, "Release notes follows organization org_7's trust profile again: Careful.")
	if r := ov.seen()[n]; r.Method != http.MethodDelete || r.URL.Query().Get("org") != ovOrg || r.URL.Query().Get("agent") != "agent_01" {
		t.Errorf("removed %s %v", r.Method, r.URL.Query())
	}
	out, _, _ = runLatere(t, "", "oversight", "profile", "reset", "--agent", "release-notes", "--json")
	var removed map[string]any
	if err := json.Unmarshal([]byte(out), &removed); err != nil || removed["agent"] != "agent_01" || removed["removed"] != true {
		t.Errorf("--json %s", out)
	}

	// A member who is no owner or admin is refused a change, with who may
	// make it.
	ov.json("PUT /profiles", http.StatusForbidden, `{"error":{"code":"forbidden","message":"No.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "profile", "set", "careful", "--agent", "release-notes"); code != exitRefused || !strings.Contains(errOut, "only an owner or an admin of organization org_7") {
		t.Errorf("a member's change: exit %d, %s", code, errOut)
	}
	// An agent the agents API does not have is refused before Limen is
	// asked.
	n = len(ov.seen())
	if _, errOut, code := runLatere(t, "", "oversight", "profile", "set", "careful", "--agent", "nobody"); code != exitRefused || !strings.Contains(errOut, `no agent "nobody" in organization org_7`) {
		t.Errorf("an unknown agent: exit %d, %s", code, errOut)
	}
	if _, errOut, code := runLatere(t, "", "oversight", "profile", "--agent", " "); code != exitRefused || !strings.Contains(errOut, "--agent needs") {
		t.Errorf("an empty agent: exit %d, %s", code, errOut)
	}
	if len(ov.seen()) != n {
		t.Error("a refused agent reached Limen")
	}
}

// In the person's own context, an agent's own profile is the person's
// subject's, and the agent follows the person's own profile without one.
func TestOversightAgentProfileInThePersonsOwnContext(t *testing.T) {
	ov, ag := newOversightStubs(t)
	ag.json("GET /agents/release-notes", http.StatusOK, ovAgent)
	ov.json("GET /profiles", http.StatusOK, ovProfile(`"org":"`+ovMe+`"`, "careful"))
	out, errOut, code := runLatere(t, "", "oversight", "profile", "--agent", "release-notes")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Release notes follows your own trust profile.")
	if q := ov.seen()[0].URL.Query(); q.Get("org") != ovMe || q.Get("agent") != "agent_01" {
		t.Errorf("query %v", q)
	}
	ov.json("PUT /profiles", http.StatusOK, ovProfile(`"org":"`+ovMe+`","agent":"agent_01"`, "strict"))
	out, _, code = runLatere(t, "", "oversight", "profile", "set", "strict", "--agent", "release-notes")
	if code != 0 || !strings.Contains(out, "Release notes now has its own trust profile: Strict") {
		t.Errorf("set: exit %d, %s", code, out)
	}
	ov.handle("DELETE /profiles", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	ov.json("GET /profiles", http.StatusInternalServerError, `{"error":{"code":"internal","message":"Down.","details":{}}}`)
	out, _, code = runLatere(t, "", "oversight", "profile", "reset", "--agent", "release-notes")
	if code != 0 || !strings.Contains(out, "Release notes follows your own trust profile again.\n") {
		t.Errorf("reset with what applies unread: exit %d, %s", code, out)
	}
	// A refusal of a change in the person's own context is Limen's own.
	ov.json("PUT /profiles", http.StatusForbidden, `{"error":{"code":"forbidden","message":"No.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "profile", "set", "careful", "--agent", "release-notes"); code != exitRefused || !strings.Contains(errOut, "forbidden: No.") {
		t.Errorf("forbidden: exit %d, %s", code, errOut)
	}
}

// A member reads, picks and removes their own stricter profile in the
// organization the login acts in; it is theirs, by their subject.
func TestOversightProfileForMe(t *testing.T) {
	ov, _ := newOrgOversightStubs(t)
	mine, org := "strict", "balanced"
	saved := true
	ov.handle("GET /profiles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Has("subject") && saved {
			_, _ = io.WriteString(w, ovProfile(`"org":"`+ovOrg+`","subject":"`+ovMe+`"`, mine))
			return
		}
		_, _ = io.WriteString(w, ovProfile(`"org":"`+ovOrg+`"`, org))
	})
	out, errOut, code := runLatere(t, "", "oversight", "profile", "--for-me")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Your own pick in organization org_7: Strict. Organization org_7's profile: Balanced.", "Limen asks you as Strict does, the stricter of the two")
	if q := ov.seen()[0].URL.Query(); q.Get("org") != ovOrg || q.Get("subject") != ovMe || q.Has("agent") {
		t.Errorf("query %v", q)
	}
	if q := ov.seen()[1].URL.Query(); q.Get("org") != ovOrg || q.Has("subject") {
		t.Errorf("the organization's: query %v", q)
	}
	// A pick that is not stricter changes nothing until the organization
	// picks a looser profile.
	mine, org = "careful", "strict"
	out, _, _ = runLatere(t, "", "oversight", "profile", "--for-me")
	contains(t, out, "It changes nothing now", "It applies if org_7 picks a looser profile.")
	// A pick stricter in one threshold and not the other: the stricter of
	// each applies.
	mine, org = "careful", "own"
	out, _, _ = runLatere(t, "", "oversight", "profile", "--for-me")
	contains(t, out, "Organization org_7's profile: costs of its own.", "it runs a step on its own at 90% sure you would allow it, and stops it below 10%")
	// A pick saved as Strict before Strict's costs changed is said by its
	// thresholds, never as Strict or as asking like a preset.
	mine, org = "strict-50", "balanced"
	out, _, _ = runLatere(t, "", "oversight", "profile", "--for-me")
	contains(t, out, "Your own pick in organization org_7: costs of its own. Organization org_7's profile: Balanced.",
		"it runs a step on its own at 98% sure you would allow it, and stops it below 25%", "Trust profile: Its own costs\n")
	if strings.Contains(out, "Strict") || strings.Contains(out, "asks you as") {
		t.Errorf("an earlier Strict pick: %s", out)
	}
	org, saved = "balanced", false
	out, _, _ = runLatere(t, "", "oversight", "profile", "--for-me")
	contains(t, out, "You have not made organization org_7's profile stricter for yourself", "Trust profile: Balanced\n")

	mine, org, saved = "strict", "balanced", true
	ov.json("PUT /profiles", http.StatusOK, ovProfile(`"org":"`+ovOrg+`","subject":"`+ovMe+`"`, "strict"))
	n := len(ov.seen())
	out, errOut, code = runLatere(t, "", "oversight", "profile", "set", "strict", "--for-me")
	if code != 0 {
		t.Fatalf("set: exit %d: %s", code, errOut)
	}
	contains(t, out, "Organization org_7's agents now ask you at least as Strict does. Nobody else's sessions change.", "asks you as Strict does")
	if b := ov.body(n); b["org"] != ovOrg || b["subject"] != ovMe || b["agent"] != nil {
		t.Errorf("saved %v", b)
	}

	ov.handle("DELETE /profiles", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	n = len(ov.seen())
	out, _, code = runLatere(t, "", "oversight", "profile", "reset", "--for-me")
	if code != 0 || !strings.Contains(out, "Your own pick in organization org_7 is removed") {
		t.Errorf("reset: exit %d, %s", code, out)
	}
	if q := ov.seen()[n].URL.Query(); q.Get("org") != ovOrg || q.Get("subject") != ovMe {
		t.Errorf("removed %v", q)
	}

	// The flags name one profile, and reset names one of them.
	for _, args := range [][]string{
		{"oversight", "profile", "--for-me", "--agent", "release-notes"},
		{"oversight", "profile", "reset"},
	} {
		n = len(ov.seen())
		if _, errOut, code := runLatere(t, "", args...); code != exitRefused || len(ov.seen()) != n {
			t.Errorf("%v: exit %d, %s", args, code, errOut)
		}
	}
}

// Outside an organization, --for-me and report have nothing to read, and
// say how to sign in to one.
func TestOversightOrganizationOnlyInAnOrganization(t *testing.T) {
	ov := newOversightStub(t)
	for _, args := range [][]string{
		{"oversight", "profile", "--for-me"},
		{"oversight", "profile", "set", "strict", "--for-me"},
		{"oversight", "profile", "reset", "--for-me"},
		{"oversight", "report"},
	} {
		if _, errOut, code := runLatere(t, "", args...); code != exitRefused || !strings.Contains(errOut, "latere login --org-id <id>") {
			t.Errorf("%v: exit %d, %s", args, code, errOut)
		}
	}
	if n := len(ov.seen()); n != 0 {
		t.Errorf("%d requests reached Limen", n)
	}
}
