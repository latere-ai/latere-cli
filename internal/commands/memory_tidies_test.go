// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// The ids of the tidies the stubs answer.
const (
	tidyWaiting = "ses_01JBWAIT0000000000000000AA"
	tidyProject = "ses_01JBTIDE0000000000000000BB"
	tidyNothing = "ses_01JBNONE0000000000000000CC"
	tidyRunning = "ses_01JBRUNS0000000000000000DD"
)

// The files of the memory mia before and after the waiting tidy changed
// it: a description with a colon, written in quotes, and a detail added.
const (
	miaBefore = "---\nname: Mia\ndescription: Mia is your sister.\ntype: person\nsource: you\n---\n"
	miaAfter  = "---\nname: Mia\ndescription: \"Mia is your sister. She lives in Lisbon: Alfama.\"\ntype: person\nsource: \"dream:" + tidyWaiting + "\"\n---\nMoved there in 2025.\n"
	lisbon    = "---\nname: Lisbon\ndescription: You visit Lisbon in spring.\ntype: area\nsource: \"dream:" + tidyWaiting + "\"\n---\n"
)

// tidyJSON is a run as the routes answer one, with changes when given.
func tidyJSON(t *testing.T, run map[string]any, changes ...map[string]any) string {
	t.Helper()
	full := map[string]any{"trigger_id": "trg_01JB", "kind": "memory", "scope": "you", "review": "apply",
		"counts": map[string]int{}, "created_at": "2026-10-30T02:00:00Z"}
	maps.Copy(full, run)
	if changes != nil {
		full["changes"] = changes
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// waitingTidy is the tidy of You whose two changes wait under Ask me
// first, the change to mia held because the person wrote mia, with the
// states the case gives the two.
func waitingTidy(t *testing.T, state, lisbonState, miaState string) string {
	t.Helper()
	return tidyJSON(t, map[string]any{"id": tidyWaiting, "state": state, "review": "ask",
		"counts":     map[string]int{"added": 1, "updated": 1, "waiting": 2},
		"ended_at":   "2026-10-30T02:03:00Z",
		"expires_at": "2026-11-06T02:03:00Z"},
		map[string]any{"id": "lisbon", "kind": "created", "state": lisbonState, "name": "Lisbon", "description": "You visit Lisbon in spring.", "after": lisbon},
		map[string]any{"id": "mia", "kind": "changed", "state": miaState, "held": "yours", "name": "Mia", "description": "Mia is your sister.", "before": miaBefore, "after": miaAfter},
	)
}

// tidyList is the list the runs route answers: a waiting tidy of You, an
// applied one of the project Tide tables, one that changed nothing, and
// one still going.
func tidyList(t *testing.T) string {
	t.Helper()
	return `{"runs":[` + strings.Join([]string{
		tidyJSON(t, map[string]any{"id": tidyRunning, "state": "running", "created_at": "2026-10-30T03:00:00Z"}),
		tidyJSON(t, map[string]any{"id": tidyWaiting, "state": "waiting", "review": "ask",
			"counts": map[string]int{"added": 1, "updated": 1, "waiting": 2}, "ended_at": "2026-10-30T02:03:00Z"}),
		tidyJSON(t, map[string]any{"id": tidyProject, "scope": "project", "project": tideTables, "state": "applied",
			"counts": map[string]int{"added": 1, "updated": 2, "removed": 1}, "ended_at": "2026-10-29T02:04:00Z"}),
		tidyJSON(t, map[string]any{"id": tidyNothing, "state": "applied", "ended_at": "2026-10-28T02:01:00Z"}),
	}, ",") + `]}`
}

// at is the time the RFC 3339 s names as the tidy commands show it.
func at(t *testing.T, s, layout string) string {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts.Local().Format(layout)
}

func TestMemoryTidiesList(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs", http.StatusOK, tidyList(t))
	for _, args := range [][]string{{"tidies"}, {"tidies", "list"}} {
		before := len(s.requests())
		out, _, err := runMemory(t, s, "", args...)
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out, "ID", "IN", "TIDIED", "STATE", "CHANGED",
			tidyRunning, "Tidying now",
			tidyWaiting, "Waits for you", "2 changes wait for you", at(t, "2026-10-30T02:03:00Z", tidyMinute),
			tidyProject, "Tide tables", "1 added, 2 updated, 1 removed",
			tidyNothing, "Nothing needed changing",
			"1 tidy has changes that wait for you: `latere memory tidies apply <id>` applies them",
			"`latere memory tidies show <id>` shows what a tidy changed")
		// The newest first, as the platform lists them.
		if strings.Index(out, tidyRunning) > strings.Index(out, tidyNothing) {
			t.Errorf("tidies out of the platform's order:\n%s", out)
		}
		// The project's name is read once, from the memory list.
		if got := s.requests()[before:]; !slices.Equal(got, []string{"GET /v1/account/runs", "GET /v1/account/memory"}) {
			t.Errorf("%v asked %v", args, got)
		}
	}
}

func TestMemoryTidiesListOfAProject(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs", http.StatusOK, tidyList(t))
	out, _, err := runMemory(t, s, "", "tidies", "--project", "tide TABLES")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, tidyProject, "Tide tables")
	if strings.Contains(out, tidyWaiting) || strings.Contains(out, "wait for you") {
		t.Errorf("a project's tidies show another's:\n%s", out)
	}
	if got := s.requests(); !slices.Equal(got, []string{"GET /v1/account/memory", "GET /v1/account/runs"}) {
		t.Errorf("requests = %v", got)
	}

	out, _, err = runMemory(t, s, "", "tidies", "--project", tideTables, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("--json is not JSON: %v\n%s", err, out)
	}
	if len(list.Runs) != 1 || list.Runs[0].ID != tidyProject {
		t.Errorf("--json of a project = %+v", list.Runs)
	}

	out, _, err = runMemory(t, s, "", "tidies", "--project", "Garden")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Tidy memory has not tidied the project Garden (prj_01JA4Q0Z8X3K5V7M9N1P2R4T6X) yet.")
}

func TestMemoryTidiesListEmpty(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs", http.StatusOK, `{"runs":[]}`)
	out, _, err := runMemory(t, s, "", "tidies")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Tidy memory has not tidied your memory yet.", "a night with nothing new starts nothing")
	if got := s.requests(); !slices.Equal(got, []string{"GET /v1/account/runs"}) {
		t.Errorf("an empty list read more: %v", got)
	}
	out, _, err = runMemory(t, s, "", "tidies", "--json")
	if err != nil || !strings.Contains(out, `"runs": []`) {
		t.Errorf("--json = %v\n%s", err, out)
	}
}

func TestMemoryTidiesShow(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs/"+tidyWaiting, http.StatusOK, waitingTidy(t, "waiting", "waiting", "waiting"))
	out, _, err := runMemory(t, s, "", "tidies", "show", tidyWaiting)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out,
		"tidy:       "+tidyWaiting+"\n",
		"in:         You\n",
		"state:      Waits for you\n",
		"tidied:     "+at(t, "2026-10-30T02:03:00Z", tidyMinute)+"\n",
		"review:     Ask me first\n",
		"changed:    2 changes wait for you\n",
		"These changes wait for you until "+at(t, "2026-11-06T02:03:00Z", tidyDay)+". If you do nothing, nothing changes.",
		"`latere memory tidies apply "+tidyWaiting+"` applies them",
		"\nAdded lisbon: Waits for you\n  + Name: Lisbon\n  + Memory: You visit Lisbon in spring.\n  + Group: Areas\n  + From: Tidy memory, in the tidy "+tidyWaiting+"\n",
		"\nUpdated mia: Waits for you\nYou wrote this memory, so this change waits for your OK.\n"+
			"    Name: Mia\n"+
			"  - Memory: Mia is your sister.\n"+
			"  + Memory: Mia is your sister. She lives in Lisbon: Alfama.\n"+
			"    Group: People\n"+
			"  - From: you\n"+
			"  + From: Tidy memory, in the tidy "+tidyWaiting+"\n"+
			"  +\n"+
			"  + Moved there in 2025.\n")
	// A tidy of You names no project, so no list is read.
	if got := s.requests(); !slices.Equal(got, []string{"GET /v1/account/runs/" + tidyWaiting}) {
		t.Errorf("requests = %v", got)
	}

	out, _, err = runMemory(t, s, "", "tidies", "show", tidyWaiting, "--json")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, `"held": "yours"`, `"expires_at": "2026-11-06T02:03:00Z"`)
}

// A tidy still going, one that changed nothing, and one of a project say
// so, the project by its name.
func TestMemoryTidiesShowWhatIsNotAChange(t *testing.T) {
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs/"+tidyRunning, http.StatusOK, tidyJSON(t, map[string]any{"id": tidyRunning, "state": "running"}))
	s.answer("GET /v1/account/runs/"+tidyNothing, http.StatusOK, tidyJSON(t, map[string]any{"id": tidyNothing, "state": "applied", "ended_at": "2026-10-28T02:01:00Z"}))
	s.answer("GET /v1/account/runs/"+tidyProject, http.StatusOK, tidyJSON(t, map[string]any{"id": tidyProject, "scope": "project", "project": tideTables, "state": "partly_applied",
		"counts": map[string]int{"removed": 1}},
		map[string]any{"id": "audience", "kind": "removed", "state": "applied", "name": "Audience", "description": "The audience is sailors.", "before": "---\nname: Audience\ndescription: The audience is sailors.\ntype: topic\nsensitive: true\nkept: \"yes\"\n---\n"},
		map[string]any{"id": "deadline", "kind": "changed", "state": "changed_since", "name": "Deadline", "description": "Due Friday.", "before": "Due Friday.", "after": "Due Friday, November 6."}))

	out, _, err := runMemory(t, s, "", "tidies", "show", tidyRunning)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "state:      Tidying\n", "changed:    Tidying now\n", "Tidy memory is still going through this memory.")

	out, _, err = runMemory(t, s, "", "tidies", "show", tidyNothing)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "changed:    Nothing needed changing\n", "Tidy memory went through this memory and left it as it was.")

	out, _, err = runMemory(t, s, "", "tidies", "show", tidyProject)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "in:         Tide tables ("+tideTables+")\n", "state:      Partly applied\n",
		"`latere memory tidies undo "+tidyProject+"` takes back what it applied",
		"\nRemoved audience: Applied\n  - Name: Audience\n  - Memory: The audience is sailors.\n  - Group: Topics\n  - About a sensitive topic\n  - kept: yes\n",
		"\nUpdated deadline: You changed it since\nYou changed this memory after Tidy memory read it, so it was left as it is.\n  - Due Friday.\n  + Due Friday, November 6.\n")
}

func TestReadableMemory(t *testing.T) {
	for _, c := range []struct {
		name, file string
		want       []string
	}{
		{"empty", "", nil},
		{"no header", "Just a line.\nAnd another.\n", []string{"Just a line.", "And another."}},
		{"a header never closed", "---\nname: Mia\n", []string{"---", "name: Mia"}},
		{"quotes, a mark at the start and lines the platform skips", "\ufeff---\nname: 'It''s Mia'\ndescription: \"Says \\\"hi\\\"\"\n  indented: skipped\n# a comment\nno colon\ntype: hobby\nsource: import\nsensitive: false\n---\n\n\nDetail.\n\n",
			[]string{"Name: It's Mia", `Memory: Says "hi"`, "Group: hobby", "From: an import", "", "Detail."}},
		{"a control character", "---\nname: Mi\x1b[2Ja\n---\n", []string{"Name: Mi[2Ja"}},
	} {
		if got := readableMemory(c.file); !slices.Equal(got, c.want) {
			t.Errorf("%s: readableMemory = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLineDiff(t *testing.T) {
	show := func(lines []diffLine) string {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(string(l.op) + l.text + "\n")
		}
		return b.String()
	}
	for _, c := range []struct {
		before, after []string
		want          string
	}{
		{nil, nil, ""},
		{[]string{"a", "b"}, []string{"a", "b"}, " a\n b\n"},
		{nil, []string{"a"}, "+a\n"},
		{[]string{"a"}, nil, "-a\n"},
		{[]string{"a", "b", "c"}, []string{"a", "x", "c", "d"}, " a\n-b\n+x\n c\n+d\n"},
		{[]string{"x", "a", "b"}, []string{"a", "b", "y"}, "-x\n a\n b\n+y\n"},
	} {
		if got := show(diffLines(c.before, c.after)); got != c.want {
			t.Errorf("diffLines(%q, %q) =\n%s\nwant\n%s", c.before, c.after, got, c.want)
		}
	}
	// Past the bound, before is removed whole and after added whole.
	long := make([]string, 1100)
	for i := range long {
		long[i] = "same"
	}
	got := diffLines(long, long)
	if len(got) != 2200 || got[0].op != '-' || got[1100].op != '+' {
		t.Errorf("a comparison past the bound = %d lines, first %q, middle %q", len(got), got[0].op, got[1100].op)
	}
}

func TestMemoryTidiesDecide(t *testing.T) {
	const apply = "POST /v1/account/runs/" + tidyWaiting + "/apply"
	const decline = "POST /v1/account/runs/" + tidyWaiting + "/decline"
	const undo = "POST /v1/account/runs/" + tidyWaiting + "/undo"
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs/"+tidyWaiting, http.StatusOK, waitingTidy(t, "waiting", "waiting", "waiting"))

	s.answer(apply, http.StatusOK, waitingTidy(t, "applied", "applied", "applied"))
	out, _, err := runMemory(t, s, "", "tidies", "apply", tidyWaiting)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "The 2 changes are applied.\n\nCHANGE   MEMORY  STATE\nAdded    lisbon  Applied\nUpdated  mia     Applied\n")
	if body := s.body(apply); len(body) != 0 {
		t.Errorf("an apply of every change named some: %v", body)
	}

	s.answer(apply, http.StatusOK, waitingTidy(t, "partly_applied", "waiting", "changed_since"))
	out, _, err = runMemory(t, s, "", "tidies", "apply", tidyWaiting, "mia")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Nothing was applied: 1 memory you changed since was left as it is.\n", "mia     You changed it since")
	if body := s.body(apply); !slices.Equal(anyStrings(body["changes"]), []string{"mia"}) {
		t.Errorf("apply body = %v", body)
	}

	s.answer(decline, http.StatusOK, waitingTidy(t, "declined", "declined", "declined"))
	out, _, err = runMemory(t, s, "", "tidies", "decline", tidyWaiting, "lisbon", "mia")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "The 2 changes are declined. Nothing changed in your memory.\n")
	if body := s.body(decline); !slices.Equal(anyStrings(body["changes"]), []string{"lisbon", "mia"}) {
		t.Errorf("decline body = %v", body)
	}

	// An undo of what was applied: lisbon undone, mia left as it is
	// because the person changed it since.
	s.answer("GET /v1/account/runs/"+tidyWaiting, http.StatusOK, waitingTidy(t, "applied", "applied", "applied"))
	undone := strings.TrimSuffix(waitingTidy(t, "partly_applied", "undone", "applied"), "}") + `,"moved_on":["mia"]}`
	s.answer(undo, http.StatusOK, undone)
	out, errOut, err := runMemory(t, s, "", "tidies", "undo", tidyWaiting, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "The change is undone. 1 memory you changed since was left as it is.\n",
		"lisbon  Undone\n", "mia     Left as it is: you changed it since\n")
	if errOut != "" {
		t.Errorf("--yes asked: %q", errOut)
	}
	out, _, err = runMemory(t, s, "", "tidies", "undo", tidyWaiting, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, `"moved_on": [`, `"mia"`)
}

// anyStrings is a JSON array of strings as a body decodes it.
func anyStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// Undoing more than one change asks first; one named change does not.
func TestMemoryTidiesUndoAsksFirst(t *testing.T) {
	const undo = "POST /v1/account/runs/" + tidyWaiting + "/undo"
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs/"+tidyWaiting, http.StatusOK, waitingTidy(t, "applied", "applied", "applied"))
	s.answer(undo, http.StatusOK, strings.TrimSuffix(waitingTidy(t, "undone", "undone", "undone"), "}")+`,"moved_on":[]}`)

	_, errOut, err := runMemory(t, s, "n\n", "tidies", "undo", tidyWaiting)
	if err == nil || !strings.Contains(err.Error(), "nothing was undone. Pass --yes") {
		t.Errorf("a no = %v", err)
	}
	contains(t, errOut, "Undo the 2 changes the tidy "+tidyWaiting+" applied?", "[y/N]")
	if slices.Contains(s.requests(), undo) {
		t.Errorf("a no sent the undo: %v", s.requests())
	}

	out, _, err := runMemory(t, s, "yes\n", "tidies", "undo", tidyWaiting)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "The 2 changes are undone.\n")

	if _, errOut, err := runMemory(t, s, "", "tidies", "undo", tidyWaiting, "mia"); err != nil || errOut != "" {
		t.Errorf("an undo of one change = %v, asked %q", err, errOut)
	}
}

func TestMemoryTidiesRefusals(t *testing.T) {
	refusal := func(code, message string) string {
		return `{"error":{"code":"` + code + `","message":"` + message + `","details":{}}}`
	}
	const apply = "POST /v1/account/runs/" + tidyWaiting + "/apply"
	s := newStubMemory(t)
	s.answer("GET /v1/account/runs/ses_gone", http.StatusNotFound, refusal("run_not_found", "This run is not in your list."))
	s.answer("GET /v1/account/runs/"+tidyWaiting, http.StatusOK, waitingTidy(t, "waiting", "waiting", "waiting"))

	for _, c := range []struct {
		name   string
		answer func()
		args   []string
		want   string
		absent string
	}{
		{"a tidy not there", nil, []string{"show", "ses_gone"},
			"run_not_found: You have no tidy ses_gone. `latere memory tidies` lists your tidies.", ""},
		{"a decision on a tidy not there", nil, []string{"apply", "ses_gone"},
			"run_not_found: You have no tidy ses_gone.", "may have been saved"},
		{"a memory the tidy did not change", nil, []string{"apply", tidyWaiting, "mia", "nope"},
			"the tidy " + tidyWaiting + " changed no memory nope; nothing was sent", ""},
		{"nothing waits", func() {
			s.answer(apply, http.StatusConflict, refusal("run_not_waiting", "Nothing of this run is waiting for you."))
		},
			[]string{"apply", tidyWaiting},
			"run_not_waiting: Nothing of the tidy " + tidyWaiting + " waits for you any more, so nothing was decided. `latere memory tidies show " + tidyWaiting + "`", "may have been saved"},
		{"none named waits", nil, []string{"apply", tidyWaiting, "mia"},
			"run_not_waiting: None of the changes you named waits for you any more", ""},
		{"still going", func() { s.answer(apply, http.StatusConflict, refusal("run_running", "This run is still going.")) },
			[]string{"apply", tidyWaiting}, "run_running: Tidy memory is still going through this memory, so what it changes cannot be decided yet.", ""},
		{"busy", func() { s.answer(apply, http.StatusConflict, refusal("run_busy", "This run is being updated.")) },
			[]string{"apply", tidyWaiting}, "run_busy: Another apply, decline or undo of the tidy " + tidyWaiting + " is being saved. Run the command again in a moment.", ""},
		{"a change the platform does not know", func() {
			s.answer(apply, http.StatusBadRequest, refusal("run_change_unknown", "This run made no such change."))
		},
			[]string{"apply", tidyWaiting, "mia"}, "run_change_unknown: The tidy " + tidyWaiting + " did not change every memory you named.", ""},
		{"an answer that failed", func() { s.answer(apply, http.StatusInternalServerError, refusal("internal", "Something went wrong.")) },
			[]string{"apply", tidyWaiting},
			"The decision may have been saved before the answer failed. `latere memory tidies show " + tidyWaiting + "` shows where each change stands", ""},
		{"no record of tidies here", func() {
			s.answer("GET /v1/account/runs", http.StatusServiceUnavailable, refusal("runs_unavailable", "Tidying memory is not available here."))
		}, []string{}, "runs_unavailable: Tidying memory is not available here.", ""},
		{"an organization's context", func() {
			s.answer("GET /v1/account/runs", http.StatusForbidden, refusal("memory_personal_only", "Memory is kept for your personal account."))
		}, []string{}, "`latere org --personal` switches to your personal account.", ""},
	} {
		if c.answer != nil {
			c.answer()
		}
		before := len(s.requests())
		out, _, err := runMemory(t, s, "", append([]string{"tidies"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) || (c.absent != "" && strings.Contains(err.Error(), c.absent)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if out != "" {
			t.Errorf("%s: printed %q", c.name, out)
		}
		if c.name == "a memory the tidy did not change" && slices.Contains(s.requests()[before:], apply) {
			t.Errorf("%s: the apply was sent", c.name)
		}
	}
}

// A memory a tidy wrote names the tidy, in the list and in show.
func TestMemorySourceOfATidy(t *testing.T) {
	s := newStubMemory(t)
	s.setList(strings.Replace(memoryList, `"source":"you","checksum":"c2"`, `"source":"dream:`+tidyWaiting+`","checksum":"c2"`, 1))
	out, _, err := runMemory(t, s, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "tidy "+tidyWaiting)
	if strings.Contains(out, "dream:") {
		t.Errorf("the list shows the source as sent:\n%s", out)
	}
	s.answer("GET /v1/account/memory/you/mia", http.StatusOK, strings.Replace(memoryOf("you", "", "mia", "person"), `"source":"you"`, `"source":"dream:`+tidyWaiting+`"`, 1))
	out, _, err = runMemory(t, s, "", "show", "mia")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "from:       Tidy memory, in the tidy "+tidyWaiting+"\n")
	if tidyOf("ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX") != "" || memorySource("ses_x") != "the conversation ses_x" {
		t.Error("a conversation's source reads as a tidy's")
	}
}
