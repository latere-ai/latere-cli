// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"latere.ai/x/sdk-go/oversight"

	"github.com/latere-ai/latere-cli/internal/api"
)

// ovMe is the subject of the login the oversight tests sign in with.
const ovMe = "https://auth.example|usr_42"

// apiStub is one product of the API under its base path: each route the
// test serves is a handler keyed "METHOD /path" below the base path; every
// request is recorded with its bearer and its body, and one it serves no
// route for is answered 404 in the API's envelope.
type apiStub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	requests []*http.Request
	bodies   []string
	bearers  []string
}

// newAPIStub serves basePath, and points envURL at it and envToken at a
// bearer the commands present as given.
func newAPIStub(t *testing.T, basePath, envURL, envToken string) *apiStub {
	t.Helper()
	s := &apiStub{t: t, routes: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, basePath)
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(r.Context()))
		s.bodies = append(s.bodies, string(body))
		s.bearers = append(s.bearers, r.Header.Get("Authorization"))
		h := s.routes[r.Method+" "+path]
		s.mu.Unlock()
		if !ok || h == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"Nothing is here.","details":{"retryable":false}}}`)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	t.Setenv(envURL, s.srv.URL+basePath)
	t.Setenv(envToken, "stub-token")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	return s
}

// json serves body with status at "METHOD /path".
func (s *apiStub) json(route string, status int, body string) {
	s.handle(route, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (s *apiStub) handle(route string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = h
}

func (s *apiStub) seen() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *apiStub) body(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(s.bodies[i]), &m); err != nil {
		s.t.Fatalf("request %d's body %q: %v", i, s.bodies[i], err)
	}
	return m
}

// saveLogin saves a login whose token names claims.
func saveLogin(t *testing.T, claims map[string]any) {
	t.Helper()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	if err := api.SaveAuthToken(api.Token{AccessToken: fakeJWT(t, claims), TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
}

// newOversightStub is the oversight API, with a person signed in, and an
// agents API that has no session, so a step is in the person's own context.
func newOversightStub(t *testing.T) *apiStub {
	t.Helper()
	ov, _ := newOversightStubs(t)
	return ov
}

// newOversightStubs is the oversight API, with a person signed in, and the
// agents API, which the commands read a step's organization from.
func newOversightStubs(t *testing.T) (ov, ag *apiStub) {
	t.Helper()
	saveLogin(t, map[string]any{"iss": "https://auth.example", "sub": "usr_42"})
	return newAPIStub(t, oversight.BasePath, envOversightURL, envOversightToken), newAgentsStub(t)
}

// ovOrg is the organization whose agent made the steps of ses_O.
const ovOrg = "org_7"

// serveOrgSession serves ses_O's log on the agents API: toolu_1 decided in
// the organization ovOrg, toolu_2 in the person's own context by name,
// toolu_3 recorded before a call named its organization, and toolu_4 in
// the person's context with the issuer rendered with its slash.
func serveOrgSession(ag *apiStub) {
	ag.json("GET /sessions/ses_O/events", http.StatusOK, `{"items":[`+
		agEvent(1, "agent.tool_use", "", `{"tool_use_id":"toolu_1","name":"bash","input":{},"verdict":"ask","decision_org":"`+ovOrg+`"}`)+`,`+
		agEvent(2, "agent.tool_use", "", `{"tool_use_id":"toolu_2","name":"bash","input":{},"verdict":"allow","decision_org":"`+ovMe+`"}`)+`,`+
		agEvent(3, "agent.tool_use", "", `{"tool_use_id":"toolu_3","name":"bash","input":{},"verdict":"allow"}`)+`,`+
		agEvent(4, "agent.tool_use", "", `{"tool_use_id":"toolu_4","name":"bash","input":{},"verdict":"allow","decision_org":"https://auth.example/|usr_42"}`)+`]}`)
}

// runLatere runs `latere args...` with stdin and returns its output, its
// error output and the exit code the process would have.
func runLatere(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	root := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	if err := root.ExecuteContext(context.Background()); err != nil {
		code = HandleExitError(&errOut, err)
	}
	return out.String(), errOut.String(), code
}

// ovCase is a case of the action id, decided allow, with a suggestion when
// verdict is not empty and an answer when answered.
func ovCase(id, verdict string, answered bool) string {
	c := `{"action":{"org":"` + ovMe + `","subject":"` + ovMe + `","action_id":"` + id + `","kind":"tool","name":"bash","effect":"write"}`
	if verdict != "" {
		c += `,"prediction":{"verdict":"` + verdict + `","approve":0.55,"uncertainty":0.4,"evidence_ratio":0.8,"ask_kind":"split","thresholds":{"allow_above":0.9,"block_below":0.25},"audit_rate":0.05,"source":"learned/3","reason":"r"}`
	}
	c += `,"decision":{"verdict":"ask","review_probability":1,"source":"learned/3"}`
	if answered {
		c += `,"answers":[{"approve":true,"by":"` + ovMe + `","at":"2026-11-01T09:00:00Z","review":true}]`
	}
	return c + `}`
}

// The cases are read a page at a time to their end, since Limen pages
// oldest first; --session keeps one session's, --limit the latest, and
// every call names the signed-in person as the org and the subject.
func TestOversightCases(t *testing.T) {
	s := newOversightStub(t)
	s.handle("GET /cases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "p2" {
			_, _ = io.WriteString(w, `{"entries":[`+ovCase("ses_A/toolu_3", "ask", true)+`,`+ovCase("ses_B/toolu_1", "", false)+`]}`)
			return
		}
		_, _ = io.WriteString(w, `{"entries":[`+ovCase("ses_A/toolu_1", "allow", false)+`,`+ovCase("ses_A/toolu_2", "", false)+`],"next_cursor":"p2"}`)
	})
	out, errOut, code := runLatere(t, "", "oversight", "cases", "--session", "ses_A", "--limit", "2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "ACTION", "SUGGESTED", "ses_A/toolu_2", "ses_A/toolu_3", "tool bash", "ask", "55%", "0.40", "split", "100%", "learned/3", "allow (check)",
		"The latest 2 of 3.")
	if strings.Contains(out, "ses_A/toolu_1") || strings.Contains(out, "ses_B") {
		t.Errorf("an older case or another session's is listed:\n%s", out)
	}
	reqs := s.seen()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want both pages", len(reqs))
	}
	q := reqs[0].URL.Query()
	if q.Get("org") != ovMe || q.Get("subject") != ovMe || q.Get("limit") != "500" || q.Has("pending") {
		t.Errorf("query %v", q)
	}
	if s.bearers[0] != "Bearer stub-token" {
		t.Errorf("bearer %q", s.bearers[0])
	}

	out, _, code = runLatere(t, "", "oversight", "cases", "--json", "--limit", "1")
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
		t.Fatalf("--json %q: %v, exit %d", out, err, code)
	}
	if len(got) != 1 || got[0]["action"].(map[string]any)["action_id"] != "ses_B/toolu_1" {
		t.Errorf("--json %v", got)
	}
}

func TestOversightCasesPending(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /cases", http.StatusOK, `{"entries":[]}`)
	out, _, code := runLatere(t, "", "oversight", "cases", "--pending")
	if code != 0 || !strings.Contains(out, "No steps wait for you to check them.") {
		t.Errorf("exit %d: %s", code, out)
	}
	if !s.seen()[0].URL.Query().Has("pending") {
		t.Error("--pending is not sent")
	}
	if _, errOut, code := runLatere(t, "", "oversight", "cases", "--limit", "-1"); code != exitRefused || !strings.Contains(errOut, "--limit") {
		t.Errorf("a negative limit: exit %d, %s", code, errOut)
	}
}

const ovExplanation = `{"org":"` + ovMe + `","subject":"` + ovMe + `","action_id":"ses_A/toolu_3","model":{"source":"learned/4","answers":40},"predicted_by":{"source":"learned/3","answers":38},
"approve":0.52,"uncertainty":0.4,"verdict":"ask","mean":0.07,"parts":{"action":0.05,"recent":0.1,"interaction":-0.08},"evidence_ratio":0.81,"ask_kind":"split",
"thresholds":{"allow_above":0.9,"block_below":0.25},
"evidence":[{"action_id":"ses_1/toolu_9","label":"bash npm install","at":"2026-10-30T10:00:00Z","approve":true,"share":0.19},
{"action_id":"ses_2/toolu_4","label":"bash npm install","at":"2026-10-29T10:00:00Z","approve":false,"share":0.18}]}`

func TestOversightExplain(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /explanations", http.StatusOK, ovExplanation)
	out, errOut, code := runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "ask, 52% likely you allow it (uncertainty 0.40)", "stops below 25%, runs above 90%", "split: your answers about steps like this went both ways",
		"settled 81%", "steps like this +0.05, your answers lately +0.10, both together -0.08 (sum +0.07)",
		"learned/4, fitted on 40 answers", "learned/3, fitted on 38 answers; Limen has learned since",
		"19%", "allow", "bash npm install", "2026-10-30 10:00 UTC", "ses_1/toolu_9", "18%", "deny")
	if q := s.seen()[0].URL.Query(); q.Get("action_id") != "ses_A/toolu_3" || q.Get("org") != ovMe {
		t.Errorf("query %v", q)
	}
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3", "--json")
	if !strings.Contains(out, `"predicted_by"`) {
		t.Errorf("--json %s", out)
	}
	// An id that is not <session>/<tool_use_id> is refused before any request.
	n := len(s.seen())
	if _, errOut, code := runLatere(t, "", "oversight", "explain", "toolu_3"); code != exitRefused || !strings.Contains(errOut, "<session>/<tool_use_id>") {
		t.Errorf("a bad id: exit %d, %s", code, errOut)
	}
	if len(s.seen()) != n {
		t.Error("a bad id was sent")
	}
}

// A Limen from before the cover calls an ask split by its evidence alone.
// Its ask is split in the commands' words only where the answers that
// moved it went both ways, or for a kind where the person both allowed and
// denied it; otherwise it is short, with the chance and the threshold it
// falls short of.
func TestOversightAskWhoseAnswersAgree(t *testing.T) {
	s := newOversightStub(t)
	agreed := func(approve string) string {
		return `{"org":"` + ovMe + `","subject":"` + ovMe + `","action_id":"ses_A/toolu_3","model":{"source":"learned/4","answers":2},
"approve":` + approve + `,"uncertainty":1.07,"verdict":"ask","mean":0.8,"evidence_ratio":0.6,"ask_kind":"split","thresholds":{"allow_above":0.9,"block_below":0.25},
"evidence":[{"action_id":"ses_1/toolu_9","label":"publish","at":"2026-10-30T10:00:00Z","approve":true,"share":0.6},
{"action_id":"ses_1/toolu_8","label":"publish","at":"2026-10-29T10:00:00Z","approve":true,"share":0.4}]}`
	}
	s.json("GET /explanations", http.StatusOK, agreed("0.8"))
	out, errOut, code := runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Why it asks:  short: not sure enough yet: Limen is 80% sure you would allow it, and Careful runs it on its own only above 90%\n")
	if strings.Contains(out, "both ways") {
		t.Errorf("answers that agree read as both ways: %s", out)
	}
	s.json("GET /explanations", http.StatusOK, agreed("0.3"))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "short: not sure enough yet: Limen is 30% sure you would allow it, and Careful stops it only below 25%\n")
	// A deny of another kind of step is no disagreement about this one.
	s.json("GET /explanations", http.StatusOK, strings.Replace(agreed("0.8"), `"label":"publish","at":"2026-10-29T10:00:00Z","approve":true`, `"label":"bash rm","at":"2026-10-29T10:00:00Z","approve":false`, 1))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "short: not sure enough yet: Limen is 80% sure you would allow it, and Careful runs it on its own only above 90%")
	// An explanation without thresholds says the chance alone.
	s.json("GET /explanations", http.StatusOK, strings.Replace(agreed("0.8"), `"thresholds":{"allow_above":0.9,"block_below":0.25},`, ``, 1))
	out, _, _ = runLatere(t, "", "oversight", "explain", "ses_A/toolu_3")
	contains(t, out, "short: not sure enough yet: Limen is 80% sure you would allow it\n")
	if presetNear(oversight.Thresholds{AllowAbove: 0.98, BlockBelow: 0.25}) != "Strict" || presetNear(oversight.Thresholds{AllowAbove: 0.66, BlockBelow: 0.34}) != "Balanced" {
		t.Error("the preset nearest the thresholds")
	}

	s.json("GET /learned", http.StatusOK, `{"org":"`+ovMe+`","subject":"`+ovMe+`","model":{"source":"learned/1","answers":13},"entries":[
{"label":"publish","kind":"tool","name":"publish","approved":2,"denied":0,"approve":0.8,"uncertainty":1.07,"verdict":"ask","evidence_ratio":0.6,"ask_kind":"split","thresholds":{"allow_above":0.9,"block_below":0.25}},
{"label":"bash npm install","kind":"tool","name":"bash","approved":6,"denied":5,"approve":0.52,"uncertainty":0.3,"verdict":"ask","evidence_ratio":0.8,"ask_kind":"split","thresholds":{"allow_above":0.9,"block_below":0.25}}],"recent":[]}`)
	out, _, _ = runLatere(t, "", "oversight", "learned")
	var publish, install string
	for line := range strings.Lines(out) {
		switch {
		case strings.HasPrefix(line, "publish"):
			publish = line
		case strings.HasPrefix(line, "bash npm install"):
			install = line
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(publish), "short") {
		t.Errorf("a kind allowed alone: %q", publish)
	}
	if !strings.HasSuffix(strings.TrimSpace(install), "split") {
		t.Errorf("a kind allowed and denied: %q", install)
	}
}

func TestOversightLearned(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /learned", http.StatusOK, `{"org":"`+ovMe+`","subject":"`+ovMe+`","model":{"source":"learned/1","answers":33},"entries":[
{"label":"bash go test","kind":"tool","name":"bash","approved":31,"denied":0,"approve":0.96,"uncertainty":0.2,"verdict":"allow","evidence_ratio":0.9,"thresholds":{"allow_above":0.9,"block_below":0.25}},
{"label":"bash git push","kind":"tool","name":"bash","approved":2,"denied":1,"approve":0.58,"uncertainty":1.1,"verdict":"ask","evidence_ratio":0.2,"ask_kind":"unsure","thresholds":{"allow_above":0.9,"block_below":0.25}}],
"recent":[{"t":10,"at":"2026-10-20T09:00:00Z","value":-0.2},{"t":40,"at":"2026-11-01T09:00:00Z","value":0.4}]}`)
	out, errOut, code := runLatere(t, "", "oversight", "learned")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "bash go test", "31", "96%", "[-----|------------|*-]", "[-----|------*-----|--]", "allow", "bash git push", "58%", "1.10", "unsure",
		"learned/1, fitted on 33 answers", "Lately you let steps run more than usual (+0.40 now, -0.20 at 2026-10-20 09:00 UTC)", "Trend, oldest to now: -0.20 +0.40")

	s.json("GET /learned", http.StatusOK, `{"org":"`+ovMe+`","subject":"`+ovMe+`","model":{"source":"learned/1","answers":0},"entries":[],"recent":[]}`)
	out, _, _ = runLatere(t, "", "oversight", "learned")
	contains(t, out, "Limen has learned nothing about you yet")
}

func TestOversightReliability(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /reliability", http.StatusOK, `{"org":"`+ovMe+`","subject":"`+ovMe+`","of":"rules/1","from":"2026-10-01T00:00:00Z","to":"2026-11-01T00:00:00Z",
"false_allow":{"value":0.02,"interval":[0,0.09],"covered":0.6,"reviews":40,"audits":12,"unanswered":3},
"false_block":{"value":null,"covered":0,"reviews":0,"audits":0,"unanswered":0},
"certificate":{"alpha":0.01,"delta":0.05,"clean":120,"needed":299,"holds":false}}`)
	out, errOut, code := runLatere(t, "", "oversight", "reliability", "--of", "rules/1", "--from", "2026-10-01", "--to", "2026-11-01T00:00:00Z")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "How often rules/1 went against you", "2% (95%: 0% to 9%), from 40 checks (12 picked at random), covering 60%", "3 shown and not checked yet",
		"unknown, since nothing it stopped was shown to you", "not yet. 120 checked steps in a row you agreed with, of 299 needed")
	q := s.seen()[0].URL.Query()
	if q.Get("of") != "rules/1" || q.Get("from") != "2026-10-01T00:00:00Z" || q.Get("to") != "2026-11-01T00:00:00Z" {
		t.Errorf("query %v", q)
	}

	// The window defaults to the 30 days before now, of Limen's own
	// suggestions.
	if _, errOut, code := runLatere(t, "", "oversight", "reliability"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	q = s.seen()[1].URL.Query()
	from, _ := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, _ := time.Parse(time.RFC3339Nano, q.Get("to"))
	if q.Get("of") != "learned" || to.Sub(from) != reliabilityWindow || time.Since(to) > time.Minute {
		t.Errorf("default query %v", q)
	}

	if _, errOut, code := runLatere(t, "", "oversight", "reliability", "--from", "last week"); code != exitRefused || !strings.Contains(errOut, "--from") {
		t.Errorf("a bad --from: exit %d, %s", code, errOut)
	}
	if _, errOut, code := runLatere(t, "", "oversight", "reliability", "--from", "2026-11-02", "--to", "2026-11-01"); code != exitRefused || !strings.Contains(errOut, "not before") {
		t.Errorf("a window backwards: exit %d, %s", code, errOut)
	}
}

func TestOversightProfile(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /profiles", http.StatusOK, `{"org":"`+ovMe+`","costs":{"false_allow":10,"false_block":4,"ask":1},"audit_rate":0.05,"thresholds":{"allow_above":0.9,"block_below":0.25},"default":true}`)
	out, errOut, code := runLatere(t, "", "oversight", "profile")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Trust profile: Careful (you have not chosen one", "at 90% sure", "below 25%", "a wrong run 10, a wrong stop 4, a question 1", "1 in 20")
	if q := s.seen()[0].URL.Query(); q.Get("org") != ovMe || q.Has("kind") || q.Has("agent") {
		t.Errorf("query %v", q)
	}

	s.json("PUT /profiles", http.StatusOK, `{"org":"`+ovMe+`","costs":{"false_allow":2.857,"false_block":2.857,"ask":1},"audit_rate":0.05,"thresholds":{"allow_above":0.65,"block_below":0.35}}`)
	out, errOut, code = runLatere(t, "", "oversight", "profile", "set", "Balanced")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Trust profile: Balanced\n", "at 65% sure", "below 35%")
	b := s.body(1)
	costs, _ := b["costs"].(map[string]any)
	if b["org"] != ovMe || b["audit_rate"] != 0.05 || costs["false_allow"] != 2.857 || costs["false_block"] != 2.857 || costs["ask"] != 1.0 {
		t.Errorf("put %v", b)
	}
	for name, want := range map[string][2]float64{"careful": {10, 4}, "strict": {50, 4}} {
		if _, errOut, code := runLatere(t, "", "oversight", "profile", "set", name, "--json"); code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, errOut)
		}
		b := s.body(len(s.seen()) - 1)
		costs, _ := b["costs"].(map[string]any)
		if costs["false_allow"] != want[0] || costs["false_block"] != want[1] || costs["ask"] != 1.0 || b["audit_rate"] != 0.05 {
			t.Errorf("%s put %v", name, b)
		}
	}
	n := len(s.seen())
	if _, errOut, code := runLatere(t, "", "oversight", "profile", "set", "reckless"); code != exitRefused || !strings.Contains(errOut, "careful, balanced or strict") {
		t.Errorf("an unknown profile: exit %d, %s", code, errOut)
	}
	if len(s.seen()) != n {
		t.Error("an unknown profile was sent")
	}
}

// An answer is sent once, whatever the API says; a step with no decision
// is a refusal that says so.
func TestOversightAnswer(t *testing.T) {
	s := newOversightStub(t)
	s.handle("POST /answers", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	out, errOut, code := runLatere(t, "", "oversight", "answer", "ses_A/toolu_3", "deny")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	contains(t, out, "Answered deny for ses_A/toolu_3.")
	if b := s.body(0); b["org"] != ovMe || b["subject"] != ovMe || b["action_id"] != "ses_A/toolu_3" || b["approve"] != false {
		t.Errorf("body %v", b)
	}
	out, _, _ = runLatere(t, "", "oversight", "answer", "ses_A/toolu_3", "allow", "--json")
	if b := s.body(1); b["approve"] != true || !strings.Contains(out, `"approve": true`) {
		t.Errorf("body %v, out %s", b, out)
	}

	s.json("POST /answers", http.StatusConflict, `{"error":{"code":"no_decision","message":"No decision.","details":{}}}`)
	n := len(s.seen())
	if _, errOut, code := runLatere(t, "", "oversight", "answer", "ses_A/toolu_9", "allow"); code != exitRefused || !strings.Contains(errOut, "no decision recorded") {
		t.Errorf("no decision: exit %d, %s", code, errOut)
	}
	s.json("POST /answers", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"Down.","details":{"retryable":true}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "answer", "ses_A/toolu_9", "allow"); code != exitNoAnswer {
		t.Errorf("unavailable: exit %d, %s", code, errOut)
	}
	if got := len(s.seen()) - n; got != 2 {
		t.Errorf("%d requests for two answers, want each sent once", got)
	}
	if _, errOut, code := runLatere(t, "", "oversight", "answer", "ses_A/toolu_9", "maybe"); code != exitRefused || !strings.Contains(errOut, "allow or deny") {
		t.Errorf("a bad answer: exit %d, %s", code, errOut)
	}
}

// Forgetting asks for yes typed in full unless --yes is given.
func TestOversightForget(t *testing.T) {
	s := newOversightStub(t)
	s.handle("POST /subjects/forget", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	_, errOut, code := runLatere(t, "y\n", "oversight", "forget")
	if code != exitRefused || !strings.Contains(errOut, "Type yes") || !strings.Contains(errOut, "nothing was removed") || len(s.seen()) != 0 {
		t.Errorf("y: exit %d, %s, %d requests", code, errOut, len(s.seen()))
	}
	out, _, code := runLatere(t, "yes\n", "oversight", "forget")
	if code != 0 || !strings.Contains(out, "Limen forgot everything") {
		t.Errorf("yes: exit %d, %s", code, out)
	}
	if b := s.body(0); b["org"] != ovMe || b["subject"] != ovMe {
		t.Errorf("body %v", b)
	}
	out, errOut, code = runLatere(t, "", "oversight", "forget", "--yes", "--json")
	if code != 0 || strings.Contains(errOut, "Type yes") || !strings.Contains(out, `"forgotten": true`) {
		t.Errorf("--yes: exit %d, %s, %s", code, out, errOut)
	}
}

// The person is the bearer's when it is a JWT, in any context, and the
// saved login's otherwise; with neither, the command says to sign in.
func TestOversightPerson(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /learned", http.StatusOK, `{"entries":[],"recent":[],"model":{"source":"learned/1","answers":0}}`)
	t.Setenv(envOversightToken, fakeJWT(t, map[string]any{"iss": "https://auth.other/", "sub": "usr_7", "org_id": "org_1"}))
	if _, errOut, code := runLatere(t, "", "oversight", "learned"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if q := s.seen()[0].URL.Query(); q.Get("org") != "https://auth.other|usr_7" || q.Get("subject") != "https://auth.other|usr_7" {
		t.Errorf("query %v", q)
	}

	t.Setenv(envOversightToken, "")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "none.json"))
	if _, errOut, code := runLatere(t, "", "oversight", "learned"); code != exitRefused || !strings.Contains(errOut, "latere login") {
		t.Errorf("signed out: exit %d, %s", code, errOut)
	}
}

func TestOversightRefusals(t *testing.T) {
	s := newOversightStub(t)
	s.json("GET /learned", http.StatusUnauthorized, `{"error":{"code":"unauthenticated","message":"Who are you?","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "learned"); code != exitRefused || !strings.Contains(errOut, "unauthenticated: Who are you?") || !strings.Contains(errOut, "latere login") {
		t.Errorf("401: exit %d, %s", code, errOut)
	}
	s.json("GET /learned", http.StatusForbidden, `{"error":{"code":"forbidden","message":"Not yours.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "learned"); code != exitRefused || !strings.Contains(errOut, "forbidden: Not yours.") {
		t.Errorf("403: exit %d, %s", code, errOut)
	}
	t.Setenv(envOversightURL, s.srv.URL+"/v1/limen")
	if _, errOut, code := runLatere(t, "", "oversight", "learned"); code != exitRefused || !strings.Contains(errOut, "not an oversight API base URL") {
		t.Errorf("a wrong base: exit %d, %s", code, errOut)
	}
}

// A step of an organization's agent is explained and answered in the
// organization its session's log names for it; a step that names the
// person, or none as an older one does, in the person's own context. A
// session the agents API does not have is the person's own.
func TestOversightStepInAnOrganization(t *testing.T) {
	ov, ag := newOversightStubs(t)
	serveOrgSession(ag)
	ov.json("GET /explanations", http.StatusOK, ovExplanation)
	ov.handle("POST /answers", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	for _, step := range []struct{ id, org string }{
		{"ses_O/toolu_1", ovOrg}, {"ses_O/toolu_2", ovMe}, {"ses_O/toolu_3", ovMe}, {"ses_O/toolu_4", ovMe}, {"ses_X/toolu_1", ovMe},
	} {
		n := len(ov.seen())
		if _, errOut, code := runLatere(t, "", "oversight", "explain", step.id); code != 0 || errOut != "" {
			t.Fatalf("explain %s: exit %d: %s", step.id, code, errOut)
		}
		if q := ov.seen()[n].URL.Query(); q.Get("org") != step.org || q.Get("subject") != ovMe || q.Get("action_id") != step.id {
			t.Errorf("explain %s: query %v, want org %s", step.id, q, step.org)
		}
		n = len(ov.seen())
		if _, errOut, code := runLatere(t, "", "oversight", "answer", step.id, "allow"); code != 0 {
			t.Fatalf("answer %s: exit %d: %s", step.id, code, errOut)
		}
		if b := ov.body(n); b["org"] != step.org || b["subject"] != ovMe || b["action_id"] != step.id {
			t.Errorf("answer %s: body %v, want org %s", step.id, b, step.org)
		}
	}

	// Limen refuses an organization's step to a login in another context,
	// and the command names the login it answers to.
	ov.json("GET /explanations", http.StatusForbidden, `{"error":{"code":"forbidden","message":"Not yours.","details":{}}}`)
	if _, errOut, code := runLatere(t, "", "oversight", "explain", "ses_O/toolu_1"); code != exitRefused || !strings.Contains(errOut, "latere login --org-id "+ovOrg) {
		t.Errorf("forbidden in an organization: exit %d, %s", code, errOut)
	}
	if _, errOut, code := runLatere(t, "", "oversight", "explain", "ses_O/toolu_2"); code != exitRefused || strings.Contains(errOut, "--org-id") {
		t.Errorf("forbidden in the person's own context: exit %d, %s", code, errOut)
	}
}

// A log the agents API does not answer leaves every step in the person's
// own context, with a note that says so.
func TestOversightStepWhenTheSessionCannotBeRead(t *testing.T) {
	ov, ag := newOversightStubs(t)
	ag.json("GET /sessions/ses_O/events", http.StatusBadRequest, `{"error":{"code":"invalid_request","message":"No.","details":{}}}`)
	ov.json("GET /explanations", http.StatusOK, ovExplanation)
	_, errOut, code := runLatere(t, "", "oversight", "explain", "ses_O/toolu_1")
	if code != 0 || !strings.Contains(errOut, "Could not read session ses_O") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if q := ov.seen()[0].URL.Query(); q.Get("org") != ovMe {
		t.Errorf("query %v", q)
	}
}

// --session reads the session's steps in each context they are in, the
// person's own and the organization's, and lists them in the log's order.
func TestOversightCasesOfASessionInAnOrganization(t *testing.T) {
	ov, ag := newOversightStubs(t)
	serveOrgSession(ag)
	ov.handle("GET /cases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("org") == ovOrg {
			_, _ = io.WriteString(w, `{"entries":[`+ovCase("ses_O/toolu_1", "ask", false)+`,`+ovCase("ses_P/toolu_1", "ask", false)+`]}`)
			return
		}
		_, _ = io.WriteString(w, `{"entries":[`+ovCase("ses_O/toolu_3", "allow", false)+`,`+ovCase("ses_O/toolu_2", "allow", true)+`]}`)
	})
	out, errOut, code := runLatere(t, "", "oversight", "cases", "--session", "ses_O", "--pending")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	first, second, third := strings.Index(out, "ses_O/toolu_1"), strings.Index(out, "ses_O/toolu_2"), strings.Index(out, "ses_O/toolu_3")
	if first < 0 || second < first || third < second || strings.Contains(out, "ses_P") {
		t.Errorf("the session's steps are not listed in the log's order:\n%s", out)
	}
	var orgs []string
	for _, r := range ov.seen() {
		q := r.URL.Query()
		if q.Get("subject") != ovMe || !q.Has("pending") {
			t.Errorf("query %v", q)
		}
		orgs = append(orgs, q.Get("org"))
	}
	if !slices.Equal(orgs, []string{ovMe, ovOrg}) {
		t.Errorf("contexts read %v, want the person's own and then %s", orgs, ovOrg)
	}

	// Without --session, the person's own context alone is read.
	n := len(ov.seen())
	if _, errOut, code := runLatere(t, "", "oversight", "cases"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if reqs := ov.seen()[n:]; len(reqs) != 1 || reqs[0].URL.Query().Get("org") != ovMe {
		t.Errorf("%d requests, want one in the person's own context", len(reqs))
	}
}

// An organization's step names whose trust profile applied to it, from
// the scope its recorded prediction names; a step in the person's own
// context reads no case and names none.
func TestOversightExplainNamesTheProfileThatApplied(t *testing.T) {
	ov, ag := newOversightStubs(t)
	serveOrgSession(ag)
	ov.json("GET /explanations", http.StatusOK, ovExplanation)
	for _, c := range []struct{ profile, want string }{
		{`{"org":"` + ovOrg + `"}`, "Profile:      organization org_7's\n"},
		{`{"org":"` + ovOrg + `","kind":"tool"}`, "Profile:      organization org_7's\n"},
		{`{"org":"` + ovOrg + `","agent":"agent_01"}`, "Profile:      the agent's own in organization org_7\n"},
		{`{"org":"` + ovOrg + `","agent":"agent_01","subject":"` + ovMe + `"}`, "Profile:      stricter, as you picked with 'latere oversight profile set --for-me'\n"},
	} {
		ov.json("GET /cases", http.StatusOK, `{"entries":[{"action":{"org":"`+ovOrg+`","subject":"`+ovMe+`","action_id":"ses_O/toolu_1","kind":"tool","name":"bash","effect":"write"},
"prediction":{"verdict":"ask","approve":0.5,"uncertainty":0.4,"thresholds":{"allow_above":0.9,"block_below":0.25},"profile":`+c.profile+`,"audit_rate":0.05,"source":"learned/4"}}]}`)
		n := len(ov.seen())
		out, errOut, code := runLatere(t, "", "oversight", "explain", "ses_O/toolu_1")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		contains(t, out, c.want)
		if q := ov.seen()[n+1].URL.Query(); ov.seen()[n+1].URL.Path != "/v1/oversight/cases" || q.Get("org") != ovOrg || q.Get("subject") != ovMe || q.Get("action_id") != "ses_O/toolu_1" || q.Get("limit") != "1" {
			t.Errorf("case read %s %v", ov.seen()[n+1].URL.Path, q)
		}
	}
	// A case that cannot be read leaves the line out, and the explanation
	// stands.
	ov.json("GET /cases", http.StatusForbidden, `{"error":{"code":"forbidden","message":"No.","details":{}}}`)
	out, errOut, code := runLatere(t, "", "oversight", "explain", "ses_O/toolu_1")
	if code != 0 || strings.Contains(out, "Profile:") {
		t.Errorf("a case not read: exit %d, %s%s", code, out, errOut)
	}
	// The person's own context reads no case.
	n := len(ov.seen())
	out, _, code = runLatere(t, "", "oversight", "explain", "ses_O/toolu_2")
	if code != 0 || strings.Contains(out, "Profile:") || len(ov.seen()) != n+1 {
		t.Errorf("the person's own step: exit %d, %d requests, %s", code, len(ov.seen())-n, out)
	}
}
