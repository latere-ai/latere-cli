// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// evalsDigest is a digest of 64 hexadecimal digits, each the digit d.
func evalsDigest(d string) string { return "sha256:" + strings.Repeat(d, 64) }

// evalsStub is the Evals API under its origin base path. Each route the
// test serves is a handler keyed "METHOD /path" below the base path; every
// request is recorded, with its bearer, and one it serves no route for is
// answered 404 in the API's envelope.
type evalsStub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	requests []*http.Request
	bearers  []string
}

func newEvalsStub(t *testing.T) *evalsStub {
	t.Helper()
	s := &evalsStub{t: t, routes: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, evalsBasePath)
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(context.Background()))
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
	t.Setenv(envEvalsURL, s.srv.URL+evalsBasePath)
	t.Setenv(envEvalsToken, "evals-token")
	t.Setenv("LATERE_PLATFORM_URL", "https://console.example")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	return s
}

// json serves body with status at "METHOD /path".
func (s *evalsStub) json(route string, status int, body string) {
	s.handle(route, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (s *evalsStub) handle(route string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = h
}

func (s *evalsStub) seen() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// runEvals runs `latere evals args...` and returns its output, its error
// and the exit code the process would have.
func runEvals(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	root := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"evals"}, args...))
	err := root.ExecuteContext(context.Background())
	if err != nil {
		code = HandleExitError(&errOut, err)
	}
	return out.String(), errOut.String(), code
}

const evalsSuiteVersion = `{"id":"sui_1","owner":"o-1","name":"fix-flaky-tests","digest":"sha256:9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c","current":true,
"created_at":"2026-10-20T10:00:00Z","tasks":["sha256:aaaa","sha256:bbbb"],"metrics":["builtin:completed"],"primary_metric":"tests_pass@sha256:cccc",
"instruction_note":"\u001b[31mred\u001b[0m"}`

// The definitions list their current versions with each kind's key column,
// a first page notes the next one, --all reads it, -q prints name@digest
// and --json prints the items as the API answered them.
func TestEvalsListDefinitions(t *testing.T) {
	s := newEvalsStub(t)
	s.handle("GET /suites", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "200" {
			t.Errorf("limit = %q, want the API's largest page", r.URL.Query().Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "p2" {
			_, _ = io.WriteString(w, `{"items":[{"id":"sui_2","owner":"o-1","name":"second","digest":"`+evalsDigest("2")+`","current":true,"created_at":"2026-10-20T10:00:00Z","tasks":[],"metrics":[],"primary_metric":"builtin:completed"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[`+evalsSuiteVersion+`],"next_cursor":"p2"}`)
	})
	out, errOut, code := runEvals(t, "suites", "list")
	if code != 0 {
		t.Fatalf("list = %d %q", code, errOut)
	}
	for _, want := range []string{"NAME", "TASKS", "PRIMARY METRIC", "fix-flaky-tests", "sha256:9f2c9f2c9f2c ", "tests_pass"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "second") || !strings.Contains(errOut, "--all reads every page") {
		t.Errorf("first page = %q, stderr %q, want the first page and a note of the next", out, errOut)
	}

	out, _, code = runEvals(t, "suites", "list", "--all", "-q")
	want := "fix-flaky-tests@sha256:9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c9f2c\nsecond@" + evalsDigest("2") + "\n"
	if code != 0 || out != want {
		t.Errorf("--all -q = %d %q, want %q", code, out, want)
	}

	out, _, code = runEvals(t, "suites", "list", "--json")
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); code != 0 || err != nil || len(items) != 1 || items[0]["instruction_note"] == nil {
		t.Errorf("--json = %d %q %v, want the API's items, every member kept", code, out, err)
	}

	s.json("GET /candidates", http.StatusOK, `{"items":[
{"id":"can_1","owner":"o-1","name":"opus-high","digest":"`+evalsDigest("3")+`","current":true,"created_at":"2026-10-20T10:00:00Z","kind":"topos","topos":{"agent":"`+evalsDigest("4")+`","model":"anthropic/claude-opus-5.5"}},
{"id":"can_2","owner":"o-1","name":"acme","digest":"`+evalsDigest("5")+`","current":true,"created_at":"2026-10-20T10:00:00Z","kind":"external","external":{"harness":"acme-harness","version":"1.4.0"}},
{"id":"can_3","owner":"o-1","name":"img","digest":"`+evalsDigest("6")+`","current":true,"created_at":"2026-10-20T10:00:00Z","kind":"program","program":{"image":"ghcr.io/acme/eval:1"}}]}`)
	out, _, code = runEvals(t, "candidates", "list")
	for _, want := range []string{"KIND", "RUNS", "sha256:444444444444 anthropic/claude-opus-5.5", "acme-harness 1.4.0", "ghcr.io/acme/eval:1"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("candidates list lacks %q:\n%s", want, out)
		}
	}

	s.json("GET /tasks", http.StatusOK, `{"items":[]}`)
	if out, _, code := runEvals(t, "tasks", "list"); code != 0 || out != "No tasks in your current context.\n" {
		t.Errorf("empty list = %d %q", code, out)
	}
}

// show prints one version's record and its kind's fields as YAML, without
// the escape sequences the service's text holds; --versions lists every
// version of the name.
func TestEvalsShowDefinition(t *testing.T) {
	s := newEvalsStub(t)
	s.json("GET /suites/fix-flaky-tests", http.StatusOK, evalsSuiteVersion)
	s.json("GET /suites/fix-flaky-tests/versions", http.StatusOK, `{"items":[`+evalsSuiteVersion+`,
{"id":"sui_0","owner":"o-1","name":"fix-flaky-tests","digest":"`+evalsDigest("0")+`","current":false,"created_at":"2026-10-19T10:00:00Z"}]}`)
	out, errOut, code := runEvals(t, "suites", "show", "fix-flaky-tests")
	if code != 0 {
		t.Fatalf("show = %d %q", code, errOut)
	}
	for _, want := range []string{"name:       fix-flaky-tests\n", "current:    yes\n", "created:    2026-10-20 10:00 UTC\n",
		"primary_metric: tests_pass@sha256:cccc\n", "tasks:\n  - sha256:aaaa\n", `instruction_note: "\e[31mred\e[0m"` + "\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\nid: sui_1") {
		t.Errorf("show printed an escape sequence or the record twice:\n%s", out)
	}

	out, _, code = runEvals(t, "suites", "show", "fix-flaky-tests@"+evalsDigest("0"), "--versions")
	if code != 0 || !strings.Contains(out, evalsDigest("0")+"  sui_0") || !strings.Contains(out, "current") {
		t.Errorf("--versions = %d:\n%s", code, out)
	}
	if got := s.seen(); got[len(got)-1].URL.Path != "/v1/evals/suites/fix-flaky-tests/versions" {
		t.Errorf("--versions read %s, want the name's versions", got[len(got)-1].URL.Path)
	}

	out, _, code = runEvals(t, "suites", "show", "fix-flaky-tests", "--json")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil || v["digest"] == nil || v["tasks"] == nil {
		t.Errorf("--json = %d %q %v", code, out, err)
	}
}

// evalsExperimentJSON is an experiment of two candidates with the state
// and cell counts given.
func evalsExperimentJSON(state, cells string) string {
	return `{"id":"exp_1","owner":"o-1","suite":"` + evalsDigest("1") + `","candidates":["` + evalsDigest("a") + `","` + evalsDigest("b") + `"],
"trials":10,"parallelism":4,"reference":{"candidate":"` + evalsDigest("a") + `"},"mode":"execute","state":"` + state + `","created_by":"u-2",
"budget":{"name":"eb-1","cap_usd_micro":25000000,"spent_usd_micro":41234,"remaining_usd_micro":24958766},
"progress":{"cells":` + cells + `,"trials":{"done":30,"pending":10},"outcomes":{"completed":28,"infra_failed":2}},
"metadata":{"commit":"abc123"},"created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T11:00:00Z"}`
}

// show prints the experiment with its definitions named, its budget in
// Examen's formats, its progress, the cells parked for a retry with their
// reasons, each comparison and the console's address; a definition it may
// not read keeps its digest.
func TestEvalsShowExperiment(t *testing.T) {
	s := newEvalsStub(t)
	s.json("GET /experiments/exp_1", http.StatusOK, evalsExperimentJSON("running", `{"complete":2,"blocked":1,"running":1}`))
	s.handle("GET /experiments/exp_1/cells", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "blocked" {
			t.Errorf("cells read in state %q, want blocked alone, the one the progress counts", r.URL.Query().Get("state"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"id":"cel_9","experiment":"exp_1","task":"`+evalsDigest("c")+`","candidate":"`+evalsDigest("b")+`","state":"blocked","parked_reason":"image_not_admitted"}]}`)
	})
	s.json("GET /experiments/exp_1/comparisons", http.StatusOK, `{"items":[{"id":"cmp_1","experiment":"exp_1","candidate":"`+evalsDigest("b")+`","reference_candidate":"`+evalsDigest("a")+`",
"automatic":true,"exploratory":false,"state":"complete","final":false,"variable":["model"],"confounds":["incomplete"]}]}`)
	s.json("GET /suites/"+evalsDigest("1"), http.StatusOK, `{"id":"sui_1","name":"fix-flaky-tests","digest":"`+evalsDigest("1")+`","current":true,"created_at":"2026-10-20T10:00:00Z"}`)
	s.json("GET /candidates/"+evalsDigest("a"), http.StatusOK, `{"id":"can_a","name":"opus-high","digest":"`+evalsDigest("a")+`","current":true,"created_at":"2026-10-20T10:00:00Z"}`)
	s.json("GET /candidates/"+evalsDigest("b"), http.StatusOK, `{"id":"can_b","name":"sonnet-high","digest":"`+evalsDigest("b")+`","current":true,"created_at":"2026-10-20T10:00:00Z"}`)

	out, errOut, code := runEvals(t, "experiments", "show", "exp_1")
	if code != 0 {
		t.Fatalf("show = %d %q", code, errOut)
	}
	for _, want := range []string{
		"suite:      fix-flaky-tests@" + evalsDigest("1"),
		"reference:  opus-high@" + evalsDigest("a"),
		"parallel:   4 trials at once\n",
		"budget:     $25.00 cap, $0.04123 spent, $24.96 left (eb-1)\n",
		"created by: u-2\n",
		"metadata:   commit=abc123\n",
		"cells:      blocked 1, complete 2, running 1\n",
		"trials:     done 30, pending 10\n",
		"outcomes:   completed 28, infra_failed 2\n",
		"cel_9", "sha256:cccccccccccc", "sonnet-high", "image_not_admitted",
		"cmp_1", "sonnet-high vs opus-high", "model", "incomplete", "provisional",
		"console:    https://console.example/console/evals/experiments/exp_1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	for _, r := range s.seen() {
		if r.URL.Path == "/v1/evals/tasks/"+evalsDigest("c") {
			return
		}
	}
	t.Error("show did not ask the parked cell's task for its name")
}

// Each figure is formatted as Examen's reports format a cost.
func TestFormatUSD(t *testing.T) {
	for micro, want := range map[int64]string{0: "$0.00", 1: "$0.000001000", 41234: "$0.04123", 500000: "$0.5000", 999999: "$1.00", 1000000: "$1.00", 24958766: "$24.96"} {
		if got := formatUSD(micro); got != want {
			t.Errorf("formatUSD(%d) = %q, want %q", micro, got, want)
		}
	}
}

// evalsStream serves the progress stream: each connection is answered by
// the next of conns, which writes its frames and returns to close it. The
// Last-Event-ID each connection carried is recorded.
type evalsStream struct {
	mu     sync.Mutex
	conns  []func(w io.Writer)
	resume []string
}

func (s *evalsStream) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.resume = append(s.resume, r.Header.Get("Last-Event-ID"))
	n := len(s.resume) - 1
	var conn func(io.Writer)
	if n < len(s.conns) {
		conn = s.conns[n]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if conn != nil {
		conn(w)
	}
}

func (s *evalsStream) resumed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.resume)
}

// frame is one frame of the stream, its data on one line as the API
// sends it.
func frame(id, event, data string) string {
	if id != "" {
		id = "id: " + id + "\n"
	}
	return id + "event: " + event + "\ndata: " + strings.ReplaceAll(data, "\n", "") + "\n\n"
}

// A watch follows the stream to its end across closes that read no frame,
// as a quiet experiment's stream closes at each token expiry: each
// connection resumes after the last event read, no event prints twice,
// trials print no line, and an experiment that completes with every cell
// complete exits 0.
func TestEvalsWatch(t *testing.T) {
	floor := evalsStreamFloor
	evalsStreamFloor = 0
	t.Cleanup(func() { evalsStreamFloor = floor })
	s := newEvalsStub(t)
	quiet := func(w io.Writer) { _, _ = io.WriteString(w, ": keepalive\n\n") }
	stream := &evalsStream{conns: []func(io.Writer){
		func(w io.Writer) {
			_, _ = io.WriteString(w, frame("3", "snapshot", evalsExperimentJSON("running", `{"running":2}`))+
				frame("4", "cell", `{"id":"cel_1","task":"fix@`+evalsDigest("c")+`","candidate":"opus@`+evalsDigest("a")+`","state":"grading"}`)+
				frame("5", "trial", `{"id":"tri_1","cell":"cel_1","index":0,"state":"done","outcome":"completed"}`))
		},
		quiet, quiet, quiet,
		func(w io.Writer) {
			_, _ = io.WriteString(w, frame("6", "cell", `{"id":"cel_1","task":"fix@`+evalsDigest("c")+`","candidate":"opus@`+evalsDigest("a")+`","state":"complete"}`)+
				frame("7", "comparison", `{"id":"cmp_1","candidate":"sonnet@`+evalsDigest("b")+`","reference_candidate":"opus@`+evalsDigest("a")+`","automatic":true,"final":false}`)+
				frame("8", "comparison", `{"id":"cmp_1","candidate":"sonnet@`+evalsDigest("b")+`","reference_candidate":"opus@`+evalsDigest("a")+`","automatic":true,"final":true}`)+
				frame("9", "experiment", `{"id":"exp_1","state":"complete","progress":{"cells":{"complete":2},"trials":{"done":30,"pending":10}}}`)+
				frame("", "end", `{"state":"complete"}`))
		},
	}}
	s.handle("GET /experiments/exp_1/stream", stream.serve)
	s.json("GET /experiments/exp_1", http.StatusOK, evalsExperimentJSON("complete", `{"complete":2}`))

	out, errOut, code := runEvals(t, "experiments", "watch", "exp_1")
	if code != 0 {
		t.Fatalf("watch = %d %q\n%s", code, errOut, out)
	}
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if _, rest, ok := strings.Cut(l, "  "); ok && len(l) > 8 && l[2] == ':' {
			l = rest
		}
		lines = append(lines, l)
	}
	want := []string{
		"experiment exp_1 running · cells running 2 · trials done 30 of 40",
		"cell cel_1 fix × opus: grading",
		"cell cel_1 fix × opus: complete",
		"comparison cmp_1 final: sonnet vs opus",
		"experiment exp_1 complete · cells complete 2 · trials done 30 of 40",
		"console:    https://console.example/console/evals/experiments/exp_1",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("watch printed\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if got := stream.resumed(); !slices.Equal(got, []string{"", "5", "5", "5", "5"}) {
		t.Errorf("Last-Event-ID per connection = %q, want none and then the last event read", got)
	}
}

// --json prints every frame as one JSON line, the snapshot and the end
// included.
func TestEvalsWatchJSON(t *testing.T) {
	s := newEvalsStub(t)
	stream := &evalsStream{conns: []func(io.Writer){func(w io.Writer) {
		_, _ = io.WriteString(w, frame("3", "snapshot", `{"id":"exp_1","state":"running"}`)+
			frame("4", "trial", `{"id":"tri_1","state":"done"}`)+frame("", "end", `{"state":"complete"}`))
	}}}
	s.handle("GET /experiments/exp_1/stream", stream.serve)
	s.json("GET /experiments/exp_1", http.StatusOK, evalsExperimentJSON("complete", `{"complete":2}`))
	out, errOut, code := runEvals(t, "experiments", "watch", "exp_1", "--json")
	want := `{"id":"3","event":"snapshot","data":{"id":"exp_1","state":"running"}}
{"id":"4","event":"trial","data":{"id":"tri_1","state":"done"}}
{"event":"end","data":{"state":"complete"}}
`
	if code != 0 || out != want {
		t.Errorf("--json = %d %q %q, want %q", code, out, errOut, want)
	}
}

// A watch exits by the experiment's end: 1 for one canceled or completed
// with a parked cell, 2 for an error frame that will not change, 3 for one
// that may.
func TestEvalsWatchExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name, end, cells string
		code             int
		want             string
	}{
		{"canceled", frame("", "end", `{"state":"canceled"}`), `{"canceled":2}`, 1, "exp_1 was canceled; every trial it finished is kept"},
		{"parked", frame("", "end", `{"state":"complete"}`), `{"complete":1,"over_budget":1}`, 1, "exp_1 completed with 1 parked cells: complete 1, over_budget 1"},
		{"denied", frame("", "error", `{"error":{"code":"experiment_not_found","message":"This experiment does not exist, or you do not have access to it.","details":{"retryable":false}}}`), "{}", 2, "experiment_not_found: This experiment does not exist"},
		{"unavailable", frame("", "error", `{"error":{"code":"unavailable","message":"Try again later.","details":{"retryable":true}}}`), "{}", 3, "unavailable: Try again later."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newEvalsStub(t)
			stream := &evalsStream{conns: []func(io.Writer){func(w io.Writer) { _, _ = io.WriteString(w, tc.end) }}}
			s.handle("GET /experiments/exp_1/stream", stream.serve)
			s.json("GET /experiments/exp_1", http.StatusOK, evalsExperimentJSON("complete", tc.cells))
			_, errOut, code := runEvals(t, "experiments", "watch", "exp_1")
			if code != tc.code || !strings.Contains(errOut, tc.want) {
				t.Errorf("watch = %d %q, want %d and %q", code, errOut, tc.code, tc.want)
			}
		})
	}
}

// A stream that closes at once, over and over, without a frame is not kept
// open by reconnecting forever: the watch exits 3. So does a watch whose
// --timeout passes.
func TestEvalsWatchGivesUp(t *testing.T) {
	s := newEvalsStub(t)
	s.handle("GET /experiments/exp_1/stream", (&evalsStream{}).serve)
	_, errOut, code := runEvals(t, "experiments", "watch", "exp_1")
	if code != 3 || !strings.Contains(errOut, "could not be kept open") {
		t.Errorf("closing stream = %d %q, want 3", code, errOut)
	}

	s.handle("GET /experiments/exp_1/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, frame("3", "snapshot", evalsExperimentJSON("running", `{"running":1}`)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	_, errOut, code = runEvals(t, "experiments", "watch", "exp_1", "--timeout", "300ms")
	if code != 3 || !strings.Contains(errOut, "exp_1 did not end within 300ms") {
		t.Errorf("timeout = %d %q, want 3", code, errOut)
	}
}

// A refusal prints the API's code, sentence and the details a person can
// act on, and exits 2 when the same request gets the same answer and 3
// when it may not; an API that cannot be reached exits 3.
func TestEvalsRefusalExitCodes(t *testing.T) {
	s := newEvalsStub(t)
	s.json("GET /experiments", http.StatusBadRequest, `{"error":{"code":"invalid_field","message":"A field has a value it cannot take.","details":{"field":"state","reason":"unknown_state","retryable":false}}}`)
	_, errOut, code := runEvals(t, "experiments", "list", "--state", "sideways")
	if code != 2 || errOut != "invalid_field: A field has a value it cannot take.\nfield: state\nreason: unknown_state\n" {
		t.Errorf("400 = %d %q", code, errOut)
	}
	s.json("GET /suites", http.StatusUnauthorized, `{"error":{"code":"invalid_token","message":"Sign in again.","details":{"retryable":false}}}`)
	if _, errOut, code := runEvals(t, "suites", "list"); code != 2 || !strings.Contains(errOut, "Run `latere login` and try again.") {
		t.Errorf("401 = %d %q", code, errOut)
	}
	s.json("GET /graders", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"Try again later.","details":{"retryable":true}}}`)
	if _, errOut, code := runEvals(t, "graders", "list"); code != 3 || !strings.Contains(errOut, "unavailable: Try again later.") {
		t.Errorf("503 = %d %q", code, errOut)
	}
	if _, errOut, code := runEvals(t, "experiments", "show", "exp_404"); code != 2 || !strings.Contains(errOut, "not_found") {
		t.Errorf("404 = %d %q", code, errOut)
	}

	t.Setenv(envEvalsURL, "https://api.example/v1/apps")
	if _, errOut, code := runEvals(t, "suites", "list"); code != 2 || !strings.Contains(errOut, "is not an Evals API base URL") {
		t.Errorf("wrong base = %d %q", code, errOut)
	}
	s.srv.Close()
	t.Setenv(envEvalsURL, s.srv.URL+evalsBasePath)
	if _, errOut, code := runEvals(t, "suites", "list"); code != 3 || !strings.Contains(errOut, "reach the Evals API at "+s.srv.URL+evalsBasePath) {
		t.Errorf("unreachable = %d %q", code, errOut)
	}
}

// LATERE_EVALS_TOKEN is presented as given, and no token is minted.
func TestEvalsTokenFromTheEnvironment(t *testing.T) {
	s := newEvalsStub(t)
	t.Setenv("AUTH_URL", "http://127.0.0.1:1")
	s.json("GET /metrics", http.StatusOK, `{"items":[]}`)
	if _, errOut, code := runEvals(t, "metrics", "list"); code != 0 {
		t.Fatalf("list = %d %q", code, errOut)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bearers) != 1 || s.bearers[0] != "Bearer evals-token" {
		t.Errorf("bearers = %q, want the variable's alone", s.bearers)
	}
}

// Every exit code of the group reaches the process unchanged, its message
// printed.
func TestHandleExitErrorKeepsTheEvalsCode(t *testing.T) {
	for _, code := range []int{evalsExitFailure, evalsExitRefused, evalsExitNoAnswer} {
		var buf bytes.Buffer
		err := fmt.Errorf("evals: %w", &evalsExitError{code: code, err: errors.New("why")})
		if got := HandleExitError(&buf, err); got != code || buf.String() != "evals: why\n" {
			t.Errorf("code %d = %d %q", code, got, buf.String())
		}
	}
}

// Every request the group sends is a route of the Evals API the platform
// serves, as Examen's OpenAPI document lists them.
func TestEvalsRoutesAreExamens(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "evals-routes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var routes [][]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := sc.Text(); line != "" && !strings.HasPrefix(line, "#") {
			routes = append(routes, strings.Fields(line))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	served := func(method, path string) bool {
		for _, r := range routes {
			if r[0] != method {
				continue
			}
			want, got := strings.Split(r[1], "/"), strings.Split(path, "/")
			if len(want) != len(got) {
				continue
			}
			match := true
			for i := range want {
				if !strings.HasPrefix(want[i], "{") && want[i] != got[i] {
					match = false
				}
			}
			if match {
				return true
			}
		}
		return false
	}

	s := newEvalsStub(t)
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[]}`)
	}
	version := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, evalsSuiteVersion)
	}
	for _, kind := range evalsKinds {
		s.handle("GET /"+kind.path, ok)
		s.handle("GET /"+kind.path+"/x", version)
		s.handle("GET /"+kind.path+"/x/versions", ok)
		runEvals(t, kind.path, "list")
		runEvals(t, kind.path, "show", "x")
		runEvals(t, kind.path, "show", "x", "--versions")
	}
	s.handle("GET /experiments", ok)
	s.json("GET /experiments/exp_1", http.StatusOK, evalsExperimentJSON("complete", `{"complete":1,"blocked":1,"over_budget":1}`))
	s.handle("GET /experiments/exp_1/cells", ok)
	s.handle("GET /experiments/exp_1/comparisons", ok)
	s.handle("GET /suites/"+evalsDigest("1"), version)
	s.handle("GET /candidates/"+evalsDigest("a"), version)
	s.handle("GET /candidates/"+evalsDigest("b"), version)
	s.handle("GET /experiments/exp_1/stream", (&evalsStream{conns: []func(io.Writer){func(w io.Writer) {
		_, _ = io.WriteString(w, frame("", "end", `{"state":"complete"}`))
	}}}).serve)
	runEvals(t, "experiments", "list")
	runEvals(t, "experiments", "show", "exp_1")
	runEvals(t, "experiments", "watch", "exp_1")

	seen := s.seen()
	if len(seen) < 3*len(evalsKinds)+7 {
		t.Fatalf("only %d requests reached the stub", len(seen))
	}
	for _, r := range seen {
		path, ok := strings.CutPrefix(r.URL.Path, evalsBasePath)
		if !ok || !served(r.Method, path) {
			t.Errorf("%s %s is not a route of the Evals API", r.Method, r.URL.Path)
		}
	}
}
