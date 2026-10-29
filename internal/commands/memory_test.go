// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// memoryList is a list answer in the shape the account document gives it:
// two memories of You in two groups, one of a project, and a memory of a
// type the CLI has no heading for.
const memoryList = `{"settings":{"remember":true,"sensitive":false},
"memories":[
{"scope":"you","id":"mia","name":"Mia","description":"Mia is your sister.","type":"person","source":"ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX","checksum":"c1","size":120,"updated":"2026-10-29T10:41:07Z"},
{"scope":"you","id":"short-lists","name":"Short lists","description":"Keep lists under five things.","type":"preference","source":"you","checksum":"c2","size":141,"updated":"2026-10-29T10:41:07Z"},
{"scope":"you","id":"tides","name":"Tides","description":"Reads tide tables.","type":"hobby","source":"import","checksum":"c4","size":90},
{"scope":"project","project":"prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W","id":"audience","name":"Audience","description":"The audience is sailors.","type":"topic","source":"you","checksum":"c3","size":100}],
"projects":[{"id":"prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W","name":"Tide tables"},{"id":"prj_01JA4Q0Z8X3K5V7M9N1P2R4T6X","name":"Garden"}],
"more":false,
"limits":{"max_name":100,"max_description":300,"max_bytes":8192,"max_import_lines":3,"types":["preference","profile","topic","area","person"]},
"import_prompt":"List everything you remember about me."}`

// tideTables is the id of the project memoryList names Tide tables.
const tideTables = "prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W"

// memoryOf is a memory as the routes answer one, with its body.
func memoryOf(scope, project, id, typ string) string {
	return `{"scope":"` + scope + `","project":"` + project + `","id":"` + id + `","name":"Short lists","description":"Keep lists under five things.","type":"` + typ + `","source":"you","body":"Applies to every list.","checksum":"c9","size":141,"updated":"2026-10-29T10:41:07Z"}`
}

// memoryAnswer is what the stub answers one route with.
type memoryAnswer struct {
	status int
	body   string
	header http.Header
}

// stubMemory is platformd's memory routes for the commands: it records
// each request, its body and its If-Match, and answers what the case sets,
// the list by default.
type stubMemory struct {
	srv *httptest.Server

	mu      sync.Mutex
	seen    []string
	bodies  map[string]map[string]any
	bearers []string
	answers map[string]memoryAnswer
	list    string
}

func newStubMemory(t *testing.T) *stubMemory {
	t.Helper()
	s := &stubMemory{bodies: map[string]map[string]any{}, answers: map[string]memoryAnswer{}, list: memoryList}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		var body map[string]any
		if r.Body != nil {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read %s: %v", key, err)
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("%s body: %v", key, err)
				}
			}
		}
		s.mu.Lock()
		s.seen = append(s.seen, key)
		s.bearers = append(s.bearers, r.Header.Get("Authorization"))
		if body != nil {
			s.bodies[key] = body
		}
		a, ok := s.answers[key]
		list := s.list
		s.mu.Unlock()
		if !ok && key == "GET /v1/account/memory" {
			a, ok = memoryAnswer{status: http.StatusOK, body: list}, true
		}
		if !ok {
			a = memoryAnswer{status: http.StatusNotFound, body: `{"error":{"code":"not_found","message":"No such route.","details":{}}}`}
		}
		maps.Copy(w.Header(), a.header)
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// answer sets what the stub answers key, "METHOD /path".
func (s *stubMemory) answer(key string, status int, body string) {
	s.answerWith(key, memoryAnswer{status: status, body: body})
}

// setList sets what the stub answers the list with.
func (s *stubMemory) setList(list string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = list
}

// answerWith sets the whole answer of key, its headers included.
func (s *stubMemory) answerWith(key string, a memoryAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[key] = a
}

// requests are the requests the stub was sent, as "METHOD /path".
func (s *stubMemory) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// body is the JSON body the stub was last sent on key.
func (s *stubMemory) body(key string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[key]
}

// runMemory runs `latere memory …` against the stub with a bearer
// presented as given, stdin as standard input, and answers stdout, stderr
// and the command's error.
func runMemory(t *testing.T, s *stubMemory, stdin string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("LATERE_PLATFORM_TOKEN", "stub-bearer")
	t.Setenv("LATERE_PLATFORM_URL", s.srv.URL)
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	root := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"memory"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// contains fails unless out holds every one of want.
func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestMemoryListByGroup(t *testing.T) {
	s := newStubMemory(t)
	out, _, err := runMemory(t, s, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	// The platform's order of groups, Preferences before People, and a type
	// it did not list after them, by its type.
	pref, people, hobby := strings.Index(out, "Preferences"), strings.Index(out, "People"), strings.Index(out, "hobby")
	if pref < 0 || people < pref || hobby < people {
		t.Errorf("groups out of the platform's order:\n%s", out)
	}
	contains(t, out, "GROUP", "short-lists", "Keep lists under five things.", "ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX",
		"Projects:", "Tide tables", tideTables, "latere memory list --project")
	// A project's memory is not one of yours, and a project with none is
	// not listed.
	if strings.Contains(out, "The audience is sailors.") || strings.Contains(out, "Garden") {
		t.Errorf("You's list shows a project's memory or a project without memory:\n%s", out)
	}
	if got := s.requests(); !slices.Equal(got, []string{"GET /v1/account/memory"}) {
		t.Errorf("requests = %v", got)
	}
	if s.bearers[0] != "Bearer stub-bearer" {
		t.Errorf("bearer = %q", s.bearers[0])
	}
}

func TestMemoryListSaysWhatTheListLeftOut(t *testing.T) {
	s := newStubMemory(t)
	s.setList(`{"settings":{"remember":false,"sensitive":false},"memories":[],"projects":[],"more":true,"limits":{"types":["preference"]},"import_prompt":""}`)
	out, _, err := runMemory(t, s, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Latere remembers nothing about you yet", "latere memory add <text>",
		"more files than one list reads", "Remember new things is off", "latere memory settings remember=on")
}

func TestMemoryListOfAProject(t *testing.T) {
	for _, by := range []string{"tide TABLES", tideTables} {
		t.Run(by, func(t *testing.T) {
			s := newStubMemory(t)
			out, _, err := runMemory(t, s, "", "list", "--project", by)
			if err != nil {
				t.Fatal(err)
			}
			contains(t, out, "Topics", "audience", "The audience is sailors.")
			if strings.Contains(out, "short-lists") || strings.Contains(out, "Projects:") {
				t.Errorf("a project's list shows You's memory:\n%s", out)
			}
			out, _, err = runMemory(t, s, "", "list", "--project", by, "--json")
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Memories []struct{ ID string } `json:"memories"`
				Projects []struct{ ID string } `json:"projects"`
				Limits   map[string]any        `json:"limits"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("--json: %v\n%s", err, out)
			}
			if len(got.Memories) != 1 || got.Memories[0].ID != "audience" || len(got.Projects) != 1 || got.Limits == nil {
				t.Errorf("--json of a project = %+v", got)
			}
		})
	}
	s := newStubMemory(t)
	_, _, err := runMemory(t, s, "", "list", "--project", "Nowhere")
	if err == nil || !strings.Contains(err.Error(), `no project named "Nowhere"`) {
		t.Errorf("an unknown project's name = %v", err)
	}
	s.setList(strings.Replace(memoryList, `"name":"Garden"`, `"name":"Tide tables"`, 1))
	_, _, err = runMemory(t, s, "", "list", "--project", "Tide tables")
	if err == nil || !strings.Contains(err.Error(), "2 of your projects are named") || !strings.Contains(err.Error(), "prj_01JA4Q0Z8X3K5V7M9N1P2R4T6X") {
		t.Errorf("a name two projects share = %v", err)
	}
}

func TestMemoryListJSONIsThePlatformsAnswer(t *testing.T) {
	s := newStubMemory(t)
	out, _, err := runMemory(t, s, "", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(memoryList), &want); err != nil {
		t.Fatal(err)
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Errorf("--json =\n%s\nwant\n%s", g, w)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMemoryShowPrintsTheBody(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/memory/projects/"+tideTables+"/audience", http.StatusOK,
		`{"scope":"project","project":"`+tideTables+`","id":"audience","name":"Audience","description":"The audience is sailors.","type":"topic","source":"ses_1","sensitive":true,"body":"Line one.\n\u001b[31mred\u001b[0m","checksum":"c3","size":100}`)
	out, _, err := runMemory(t, s, "", "show", "audience", "--project", "Tide tables")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "audience", "The audience is sailors.", "Topics", "Tide tables ("+tideTables+")",
		"the conversation ses_1", "sensitive:", "Line one.\n[31mred[0m")
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape a memory holds reached the terminal: %q", out)
	}
}

func TestMemoryAddSendsWhatWasTyped(t *testing.T) {
	s := newStubMemory(t)
	s.answer("POST /v1/account/memory", http.StatusCreated, memoryOf("you", "", "keep-lists-short", "person"))
	out, _, err := runMemory(t, s, "More detail.\n", "add", "--group", "People", "--name", "Short", "--body", "-", "Keep", "lists", "short.")
	if err != nil {
		t.Fatal(err)
	}
	body := s.body("POST /v1/account/memory")
	if body["description"] != "Keep lists short." || body["type"] != "person" || body["name"] != "Short" ||
		body["body"] != "More detail.\n" || body["scope"] != "you" || body["project"] != nil {
		t.Errorf("add body = %v", body)
	}
	contains(t, out, "keep-lists-short", "Applies to every list.")
	// The memory's group is left to the platform when none is named, and a
	// project named by its name is sent by its id.
	s.answer("POST /v1/account/memory", http.StatusCreated, memoryOf("project", tideTables, "a", "preference"))
	if _, _, err := runMemory(t, s, "", "add", "--project", "Tide tables", "Sailors."); err != nil {
		t.Fatal(err)
	}
	body = s.body("POST /v1/account/memory")
	if body["type"] != nil || body["scope"] != "project" || body["project"] != tideTables {
		t.Errorf("add to a project = %v", body)
	}
	if _, _, err := runMemory(t, s, "", "add", "  "); err == nil || !strings.Contains(err.Error(), "nothing was saved") {
		t.Errorf("an empty text = %v", err)
	}
}

func TestMemoryEditSendsOnlyTheFlagsGiven(t *testing.T) {
	s := newStubMemory(t)
	s.answer("PATCH /v1/account/memory/you/short-lists", http.StatusOK, memoryOf("you", "", "short-lists", "preference"))
	if _, _, err := runMemory(t, s, "", "edit", "short-lists", "--text", "Keep lists under four things."); err != nil {
		t.Fatal(err)
	}
	if body := s.body("PATCH /v1/account/memory/you/short-lists"); len(body) != 1 || body["description"] != "Keep lists under four things." {
		t.Errorf("edit body = %v", body)
	}
	if _, _, err := runMemory(t, s, "", "edit", "short-lists", "--body", "", "--group", "topics"); err != nil {
		t.Fatal(err)
	}
	if body := s.body("PATCH /v1/account/memory/you/short-lists"); len(body) != 2 || body["body"] != "" || body["type"] != "topic" {
		t.Errorf("edit body = %v", body)
	}
	before := len(s.requests())
	for _, args := range [][]string{{"edit", "short-lists"}, {"edit", "short-lists", "--text", " "}, {"edit", "short-lists", "--group", ""}} {
		if _, _, err := runMemory(t, s, "", args...); err == nil || !strings.Contains(err.Error(), "nothing was sent") {
			t.Errorf("%v = %v", args, err)
		}
	}
	if len(s.requests()) != before {
		t.Errorf("a refused edit sent %v", s.requests()[before:])
	}
}

func TestMemoryMoveBetweenScopes(t *testing.T) {
	s := newStubMemory(t)
	s.answer("PATCH /v1/account/memory/you/short-lists", http.StatusOK, memoryOf("project", tideTables, "short-lists-2", "preference"))
	out, _, err := runMemory(t, s, "", "mv", "short-lists", "Tide tables")
	if err != nil {
		t.Fatal(err)
	}
	if body := s.body("PATCH /v1/account/memory/you/short-lists"); len(body) != 2 || body["scope"] != "project" || body["project"] != tideTables {
		t.Errorf("move body = %v", body)
	}
	contains(t, out, "Moved short-lists from You to the project Tide tables", "as short-lists-2")

	s.answer("PATCH /v1/account/memory/projects/"+tideTables+"/audience", http.StatusOK, memoryOf("you", "", "audience", "topic"))
	out, _, err = runMemory(t, s, "", "mv", "audience", "YOU", "--project", tideTables)
	if err != nil {
		t.Fatal(err)
	}
	if body := s.body("PATCH /v1/account/memory/projects/" + tideTables + "/audience"); len(body) != 1 || body["scope"] != "you" {
		t.Errorf("move to You body = %v", body)
	}
	if !strings.Contains(out, "to You.") {
		t.Errorf("move to You said:\n%s", out)
	}
	// A move to where the memory is sends nothing.
	before := len(s.requests())
	out, _, err = runMemory(t, s, "", "mv", "audience", tideTables, "--project", "Tide tables")
	if err != nil || !strings.Contains(out, "already, so nothing changed") {
		t.Errorf("a move in place = %v\n%s", err, out)
	}
	for _, r := range s.requests()[before:] {
		if strings.HasPrefix(r, "PATCH") {
			t.Errorf("a move in place sent %s", r)
		}
	}
}

func TestMemoryDeleteAsksFirst(t *testing.T) {
	s := newStubMemory(t)
	s.answer("DELETE /v1/account/memory/you/short-lists", http.StatusNoContent, "")
	s.answer("DELETE /v1/account/memory", http.StatusNoContent, "")
	for _, c := range []struct {
		args  []string
		stdin string
	}{
		{[]string{"rm", "short-lists"}, ""},
		{[]string{"rm", "short-lists"}, "n\n"},
		{[]string{"forget"}, "y\n"},
	} {
		_, errOut, err := runMemory(t, s, c.stdin, c.args...)
		if err == nil || !strings.Contains(err.Error(), "nothing was deleted") || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("%v with %q = %v", c.args, c.stdin, err)
		}
		if !strings.Contains(errOut, "for good") {
			t.Errorf("%v asked:\n%s", c.args, errOut)
		}
	}
	if got := s.requests(); len(got) != 0 {
		t.Errorf("an unanswered question sent %v", got)
	}
	out, _, err := runMemory(t, s, "y\n", "rm", "short-lists")
	if err != nil || !strings.Contains(out, "Deleted short-lists from You.") {
		t.Errorf("rm after y = %v\n%s", err, out)
	}
	out, _, err = runMemory(t, s, "yes\n", "forget")
	if err != nil || !strings.Contains(out, "Forgot everything") {
		t.Errorf("forget after yes = %v\n%s", err, out)
	}
	if _, _, err := runMemory(t, s, "", "rm", "short-lists", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runMemory(t, s, "", "forget", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := s.requests(); !slices.Equal(got, []string{"DELETE /v1/account/memory/you/short-lists", "DELETE /v1/account/memory",
		"DELETE /v1/account/memory/you/short-lists", "DELETE /v1/account/memory"}) {
		t.Errorf("requests = %v", got)
	}
}

func TestMemoryRestore(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/memory/forgotten", http.StatusOK, `{"forgotten":[
{"scope":"you","id":"trains","size":120,"forgotten_at":"2026-10-29T11:02:00Z","purges_at":"2026-11-28T11:02:00Z"},
{"scope":"project","project":"`+tideTables+`","id":"boats","size":90,"forgotten_at":"2026-10-28T11:02:00Z","purges_at":"2026-11-27T11:02:00Z"}]}`)
	out, _, err := runMemory(t, s, "", "restore")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "trains", "2026-11-28", "boats", tideTables, "latere memory restore <id>")
	out, _, err = runMemory(t, s, "", "restore", "--project", "Tide tables")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "trains") || !strings.Contains(out, "Tide tables") {
		t.Errorf("a project's forgotten:\n%s", out)
	}
	out, _, err = runMemory(t, s, "", "restore", "--project", "Garden")
	if err != nil || !strings.Contains(out, "Nothing was forgotten in the project Garden") {
		t.Errorf("a project with nothing forgotten = %v\n%s", err, out)
	}

	s.answer("POST /v1/account/memory/forgotten/restore", http.StatusOK, memoryOf("you", "", "trains", "topic"))
	if _, _, err := runMemory(t, s, "", "restore", "trains"); err != nil {
		t.Fatal(err)
	}
	if body := s.body("POST /v1/account/memory/forgotten/restore"); body["scope"] != "you" || body["id"] != "trains" {
		t.Errorf("restore body = %v", body)
	}
	s.answer("POST /v1/account/memory/forgotten/restore", http.StatusConflict,
		`{"error":{"code":"memory_taken","message":"A memory with this name is there again.","details":{"id":"trains"}}}`)
	_, _, err = runMemory(t, s, "", "restore", "trains")
	if err == nil || !strings.Contains(err.Error(), "memory_taken: A memory with the id trains is in You again") {
		t.Errorf("a restore whose id is taken = %v", err)
	}
	s.answer("POST /v1/account/memory/forgotten/restore", http.StatusNotFound,
		`{"error":{"code":"memory_not_found","message":"This memory no longer exists.","details":{"id":"trains"}}}`)
	_, _, err = runMemory(t, s, "", "restore", "trains")
	if err == nil || !strings.Contains(err.Error(), "Nothing forgotten in You has the id trains") {
		t.Errorf("a restore of nothing = %v", err)
	}
}

// zipOf is a whole zip holding one file.
func zipOf(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("latere-memory/you/MEMORY.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, "# You\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestMemoryExport(t *testing.T) {
	whole := zipOf(t)
	s := newStubMemory(t)
	s.answerWith("GET /v1/account/memory/export", memoryAnswer{status: http.StatusOK, body: string(whole),
		header: http.Header{"Content-Type": {"application/zip"}, "Content-Disposition": {`attachment; filename="latere-memory-2026-10-29.zip"`}}})
	dir := t.TempDir()
	t.Chdir(dir)
	out, _, err := runMemory(t, s, "", "export")
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, "latere-memory-2026-10-29.zip")
	got, err := os.ReadFile(saved)
	if err != nil || !bytes.Equal(got, whole) {
		t.Fatalf("the saved zip = %v, %d bytes, want %d", err, len(got), len(whole))
	}
	if info, err := os.Stat(saved); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the zip's mode = %v, %v; want it readable only by its owner", info.Mode(), err)
	}
	contains(t, out, "Saved your memory to latere-memory-2026-10-29.zip")
	// A file that is there is kept, unless --force.
	if _, _, err := runMemory(t, s, "", "export"); err == nil || !strings.Contains(err.Error(), "is there already") {
		t.Errorf("an export over a file = %v", err)
	}
	before := len(s.requests())
	if _, _, err := runMemory(t, s, "", "export", "-o", saved); err == nil || len(s.requests()) != before {
		t.Errorf("an export over a named file = %v, and it asked the platform %v", err, s.requests()[before:])
	}
	if _, _, err := runMemory(t, s, "", "export", "--force"); err != nil {
		t.Fatal(err)
	}
	// A zip cut short leaves nothing behind.
	s.answerWith("GET /v1/account/memory/export", memoryAnswer{status: http.StatusOK, body: string(whole[:len(whole)-10]),
		header: http.Header{"Content-Type": {"application/zip"}}})
	_, _, err = runMemory(t, s, "", "export", "-o", "cut.zip")
	if err == nil || !strings.Contains(err.Error(), "stopped before its end") {
		t.Errorf("a cut export = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "latere-memory-2026-10-29.zip" {
			t.Errorf("a cut export left %s", e.Name())
		}
	}
	// - writes to standard output.
	s.answerWith("GET /v1/account/memory/export", memoryAnswer{status: http.StatusOK, body: string(whole), header: http.Header{"Content-Type": {"application/zip"}}})
	out, _, err = runMemory(t, s, "", "export", "-o", "-")
	if err != nil || out != string(whole) {
		t.Errorf("export to standard output = %v, %d bytes", err, len(out))
	}
}

func TestMemoryImport(t *testing.T) {
	s := newStubMemory(t)
	s.answer("POST /v1/account/memory/import", http.StatusCreated, `{"added":[{"scope":"you","id":"mia-is-my-sister","name":"Mia is my sister","description":"Mia is my sister.","type":"person","source":"import","checksum":"c","size":118}],"skipped":1}`)
	answer := "Preference: Keep lists under five things.\n\nPerson: Mia is my sister.\n"
	file := filepath.Join(t.TempDir(), "answer.txt")
	if err := os.WriteFile(file, []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runMemory(t, s, "", "import", file, "--project", "Tide tables")
	if err != nil {
		t.Fatal(err)
	}
	if body := s.body("POST /v1/account/memory/import"); body["text"] != answer || body["scope"] != "project" || body["project"] != tideTables {
		t.Errorf("import body = %v", body)
	}
	contains(t, out, "Saved 1 memory to the project Tide tables", "1 line you had already was left out", "People", "mia-is-my-sister")
	if errOut != "" {
		t.Errorf("an import within the bound said:\n%s", errOut)
	}
	// Standard input, and a text past the platform's bound, which the list
	// gives as three lines.
	long := "Topic: one\nTopic: two\nTopic: three\nTopic: four\n"
	_, errOut, err = runMemory(t, s, long, "import")
	if err != nil {
		t.Fatal(err)
	}
	if body := s.body("POST /v1/account/memory/import"); body["text"] != long || body["scope"] != "you" {
		t.Errorf("import from standard input = %v", body)
	}
	contains(t, errOut, "4 lines with something on them", "at most 3", "after the first 3")
	// The prompt is the platform's, and asks nothing else.
	before := len(s.requests())
	out, _, err = runMemory(t, s, "", "import", "--prompt")
	if err != nil || out != "List everything you remember about me.\n" {
		t.Errorf("--prompt = %v\n%s", err, out)
	}
	if got := s.requests()[before:]; !slices.Equal(got, []string{"GET /v1/account/memory"}) {
		t.Errorf("--prompt asked %v", got)
	}
	// At a terminal with no file, the prompt is shown before what is
	// pasted is read.
	defer func(was func(io.Reader) bool) { inputIsTerminal = was }(inputIsTerminal)
	inputIsTerminal = func(io.Reader) bool { return true }
	_, errOut, err = runMemory(t, s, "Profile: Lives in Lisbon.\n", "import")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, errOut, "Ask the other assistant this", "List everything you remember about me.", "Ctrl-D")
	before = len(s.requests())
	if _, _, err := runMemory(t, s, " \n", "import", "-"); err == nil || !strings.Contains(err.Error(), "nothing to import") {
		t.Errorf("an empty import = %v", err)
	}
	for _, r := range s.requests()[before:] {
		if strings.HasPrefix(r, "POST") {
			t.Errorf("an empty import sent %s", r)
		}
	}
}

func TestMemoryRefusals(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/memory/you/gone", http.StatusNotFound,
		`{"error":{"code":"memory_not_found","message":"This memory no longer exists.","details":{"id":"gone"}}}`)
	_, _, err := runMemory(t, s, "", "show", "gone")
	if err == nil || !strings.Contains(err.Error(), "memory_not_found: There is no memory gone in You. `latere memory list` shows the ids.") {
		t.Errorf("a memory not found = %v", err)
	}
	s.answer("GET /v1/account/memory/projects/prj_nope/gone", http.StatusNotFound,
		`{"error":{"code":"not_found","message":"Not found.","details":{}}}`)
	_, _, err = runMemory(t, s, "", "show", "gone", "--project", "prj_nope")
	if err == nil || !strings.Contains(err.Error(), "You have no project prj_nope") {
		t.Errorf("a project not held = %v", err)
	}
	s.answer("GET /v1/account/memory", http.StatusForbidden,
		`{"error":{"code":"memory_personal_only","message":"Memory is kept for your personal account and is not available in an organization yet.","details":{"org_id":"org_1"}}}`)
	_, _, err = runMemory(t, s, "", "list")
	if err == nil || !strings.Contains(err.Error(), "memory_personal_only:") || !strings.Contains(err.Error(), "latere org --personal") {
		t.Errorf("an organization's context = %v", err)
	}
	s.answer("GET /v1/account/memory", http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"The token was refused.","details":{}}}`)
	_, _, err = runMemory(t, s, "", "list")
	if err == nil || !strings.Contains(err.Error(), "Run `latere login`") {
		t.Errorf("a refused token = %v", err)
	}
}
