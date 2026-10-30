// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMemoryE2E drives the built binary through the memory commands
// against a stub of platformd's memory routes with auth's mint: every
// request carries the token minted for the platform's audience from the
// saved login, an add and an edit send what was typed, a move reads the
// project by its name, an import reads standard input, the settings show a
// switch the platform adds, a delete without a yes sends nothing, and a
// refusal exits 1 with its code.
func TestMemoryE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const project = "prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W"
	memoryJSON := func(scope, id string) string {
		p := ""
		if scope == "project" {
			p = project
		}
		return `{"scope":"` + scope + `","project":"` + p + `","id":"` + id + `","name":"Short lists","description":"Keep lists short.","type":"preference","source":"you","checksum":"c1","size":120,"updated":"2026-10-29T10:41:07Z"}`
	}
	var (
		mu        sync.Mutex
		requests  []string
		bodies    = map[string]map[string]any{}
		audiences []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/actor-tokens" {
			var mint struct {
				Audience string `json:"audience"`
			}
			if err := json.NewDecoder(r.Body).Decode(&mint); err != nil {
				t.Errorf("mint body: %v", err)
			}
			audiences = append(audiences, mint.Audience)
			_, _ = io.WriteString(w, `{"actor_token":"platform-actor","expires_in":300}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer platform-actor" {
			t.Errorf("%s %s carried %q, want the bearer minted for the platform", r.Method, r.URL.Path, got)
		}
		key := r.Method + " " + r.URL.Path
		requests = append(requests, key)
		if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("%s body: %v", key, err)
			}
			bodies[key] = body
		}
		switch key {
		case "GET /v1/account/memory":
			_, _ = io.WriteString(w, `{"settings":{"remember":true,"sensitive":false,"search_chats":false},
"memories":[`+memoryJSON("you", "short-lists")+`],"projects":[{"id":"`+project+`","name":"Tide tables"}],"more":false,
"limits":{"max_name":100,"max_description":300,"max_bytes":8192,"max_import_lines":200,"types":["preference","profile","topic","area","person"]},
"import_prompt":"List everything you remember about me."}`)
		case "POST /v1/account/memory":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, memoryJSON("you", "short-lists"))
		case "PATCH /v1/account/memory/you/short-lists":
			if bodies[key]["scope"] == "project" {
				_, _ = io.WriteString(w, memoryJSON("project", "short-lists"))
				return
			}
			_, _ = io.WriteString(w, memoryJSON("you", "short-lists"))
		case "POST /v1/account/memory/import":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"added":[`+memoryJSON("you", "mia-is-my-sister")+`],"skipped":0}`)
		case "GET /v1/account/memory/you/gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"memory_not_found","message":"This memory no longer exists.","details":{"id":"gone"}}}`)
		default:
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	authPath := filepath.Join(root, "auth-token.json")
	login, err := json.Marshal(map[string]any{"access_token": "saved-login", "expires_at": time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, login, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LATERE_PLATFORM_URL="+server.URL, "LATERE_PLATFORM_TOKEN=", "AUTH_URL="+server.URL,
		"LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true",
		"XDG_CONFIG_HOME="+root, "HOME="+root)
	// run answers stdout, stderr, the error, and the requests the command
	// made to the memory routes.
	run := func(stdin string, args ...string) (string, string, []string, error) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"memory"}, args...)...)
		command.Env, command.Dir = env, root
		command.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		mu.Lock()
		defer mu.Unlock()
		return stdout.String(), stderr.String(), slices.Clone(requests), err
	}
	body := func(key string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return bodies[key]
	}

	out, stderr, seen, err := run("", "list")
	if err != nil || !strings.Contains(out, "short-lists") || !strings.Contains(out, "Preferences") {
		t.Errorf("list = %v %q\n%s", err, stderr, out)
	}
	if !slices.Equal(seen, []string{"GET /v1/account/memory"}) {
		t.Errorf("list asked %v", seen)
	}

	if _, stderr, _, err = run("", "add", "--group", "Preferences", "Keep", "lists", "short."); err != nil {
		t.Fatalf("add: %v %q", err, stderr)
	}
	if b := body("POST /v1/account/memory"); b["description"] != "Keep lists short." || b["type"] != "preference" || b["scope"] != "you" {
		t.Errorf("add body = %v", b)
	}

	if _, stderr, _, err = run("", "edit", "short-lists", "--name", "Lists"); err != nil {
		t.Fatalf("edit: %v %q", err, stderr)
	}
	if b := body("PATCH /v1/account/memory/you/short-lists"); len(b) != 1 || b["name"] != "Lists" {
		t.Errorf("edit body = %v", b)
	}

	out, stderr, seen, err = run("", "mv", "short-lists", "Tide tables")
	if err != nil || !strings.Contains(out, "Moved short-lists from You to the project Tide tables") {
		t.Errorf("mv = %v %q\n%s", err, stderr, out)
	}
	if !slices.Equal(seen, []string{"GET /v1/account/memory", "PATCH /v1/account/memory/you/short-lists"}) {
		t.Errorf("mv asked %v", seen)
	}

	out, stderr, _, err = run("Person: Mia is my sister.\n", "import")
	if err != nil || !strings.Contains(out, "Saved 1 memory to You") {
		t.Errorf("import = %v %q\n%s", err, stderr, out)
	}
	if b := body("POST /v1/account/memory/import"); b["text"] != "Person: Mia is my sister.\n" {
		t.Errorf("import body = %v", b)
	}

	out, stderr, _, err = run("", "settings")
	if err != nil || !strings.Contains(out, "search_chats") || !strings.Contains(out, "cannot change it") {
		t.Errorf("settings = %v %q\n%s", err, stderr, out)
	}

	exitOne := func(err error) bool {
		exit, ok := errors.AsType[*exec.ExitError](err)
		return ok && exit.ExitCode() == 1
	}
	_, stderr, seen, err = run("", "rm", "short-lists")
	if !exitOne(err) || !strings.Contains(stderr, "nothing was deleted") || len(seen) != 0 {
		t.Errorf("rm without a yes = %v, sent %v\n%s", err, seen, stderr)
	}
	out, stderr, _, err = run("", "show", "gone")
	if !exitOne(err) || out != "" || !strings.Contains(stderr, "memory_not_found: There is no memory gone in You.") {
		t.Errorf("show of a memory not there = %v stdout %q stderr %q", err, out, stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, a := range audiences {
		if a != "api.latere.ai" {
			t.Errorf("minted for %q, want the platform's audience", a)
		}
	}
	if len(audiences) == 0 {
		t.Error("no token was minted from the saved login")
	}
}

// TestMemoryTidiesE2E drives the built binary through the tidies of Tidy
// memory against a stub of platformd's runs routes with auth's mint: the
// list names a project's tidy by the project's name, show prints a
// memory before and after, an apply sends the memory named, an undo of
// more than one change sends nothing without a yes and says what it left
// with one, and a tidy not there exits 1 with its code.
func TestMemoryTidiesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const (
		project = "prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W"
		tidy    = "ses_01JBWAIT0000000000000000AA"
		mia     = "---\nname: Mia\ndescription: Mia is your sister.\ntype: person\nsource: you\n---\n"
		miaNew  = "---\nname: Mia\ndescription: Mia is your sister, in Lisbon.\ntype: person\nsource: \"dream:" + tidy + "\"\n---\n"
	)
	// run answers the tidy with each change in the state given.
	run := func(state, lisbonState, miaState, extra string) string {
		changes, err := json.Marshal([]map[string]any{
			{"id": "lisbon", "kind": "created", "state": lisbonState, "name": "Lisbon", "description": "You visit Lisbon.", "after": "---\nname: Lisbon\ndescription: You visit Lisbon.\ntype: area\n---\n"},
			{"id": "mia", "kind": "changed", "state": miaState, "held": "yours", "name": "Mia", "description": "Mia is your sister, in Lisbon.", "before": mia, "after": miaNew},
		})
		if err != nil {
			t.Fatal(err)
		}
		return `{"id":"` + tidy + `","kind":"memory","scope":"you","state":"` + state + `","review":"ask",
"counts":{"added":1,"updated":1,"removed":0,"waiting":2},"created_at":"2026-10-30T02:00:00Z","ended_at":"2026-10-30T02:03:00Z",
"expires_at":"2026-11-06T02:03:00Z","changes":` + string(changes) + extra + `}`
	}
	var (
		mu       sync.Mutex
		requests []string
		bodies   = map[string]map[string]any{}
		minted   int
		applied  bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/actor-tokens" {
			minted++
			_, _ = io.WriteString(w, `{"actor_token":"platform-actor","expires_in":300}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer platform-actor" {
			t.Errorf("%s %s carried %q, want the bearer minted for the platform", r.Method, r.URL.Path, got)
		}
		key := r.Method + " " + r.URL.Path
		requests = append(requests, key)
		if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("%s body: %v", key, err)
			}
			bodies[key] = body
		}
		switch key {
		case "GET /v1/account/runs":
			_, _ = io.WriteString(w, `{"runs":[
{"id":"`+tidy+`","kind":"memory","scope":"you","state":"waiting","review":"ask","counts":{"added":1,"updated":1,"removed":0,"waiting":2},"created_at":"2026-10-30T02:00:00Z","ended_at":"2026-10-30T02:03:00Z"},
{"id":"ses_01JBTIDE0000000000000000BB","kind":"memory","scope":"project","project":"`+project+`","state":"applied","review":"apply","counts":{"added":0,"updated":2,"removed":1,"waiting":0},"created_at":"2026-10-29T02:00:00Z","ended_at":"2026-10-29T02:04:00Z"}]}`)
		case "GET /v1/account/memory":
			_, _ = io.WriteString(w, `{"settings":{"remember":true,"chats":true,"sensitive":false,"review":"ask"},"memories":[],
"projects":[{"id":"`+project+`","name":"Tide tables"}],"more":false,"limits":{"types":["preference","profile","topic","area","person"]},"import_prompt":""}`)
		case "GET /v1/account/runs/" + tidy:
			if applied {
				_, _ = io.WriteString(w, run("applied", "applied", "applied", ""))
				return
			}
			_, _ = io.WriteString(w, run("waiting", "waiting", "waiting", ""))
		case "POST /v1/account/runs/" + tidy + "/apply":
			applied = true
			_, _ = io.WriteString(w, run("partly_applied", "waiting", "applied", ""))
		case "POST /v1/account/runs/" + tidy + "/undo":
			_, _ = io.WriteString(w, run("partly_applied", "undone", "applied", `,"moved_on":["mia"]`))
		case "GET /v1/account/runs/ses_gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"run_not_found","message":"This run is not in your list.","details":{"run":"ses_gone"}}}`)
		default:
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	authPath := filepath.Join(root, "auth-token.json")
	login, err := json.Marshal(map[string]any{"access_token": "saved-login", "expires_at": time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, login, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LATERE_PLATFORM_URL="+server.URL, "LATERE_PLATFORM_TOKEN=", "AUTH_URL="+server.URL,
		"LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true",
		"XDG_CONFIG_HOME="+root, "HOME="+root)
	latere := func(stdin string, args ...string) (string, string, []string, error) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"memory", "tidies"}, args...)...)
		command.Env, command.Dir = env, root
		command.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		mu.Lock()
		defer mu.Unlock()
		return stdout.String(), stderr.String(), slices.Clone(requests), err
	}

	out, stderr, seen, err := latere("")
	if err != nil || !strings.Contains(out, "2 changes wait for you") || !strings.Contains(out, "Tide tables") || !strings.Contains(out, "2 updated, 1 removed") {
		t.Errorf("tidies = %v %q\n%s", err, stderr, out)
	}
	if !slices.Equal(seen, []string{"GET /v1/account/runs", "GET /v1/account/memory"}) {
		t.Errorf("tidies asked %v", seen)
	}

	out, stderr, _, err = latere("", "show", tidy)
	if err != nil || !strings.Contains(out, "  - Memory: Mia is your sister.\n  + Memory: Mia is your sister, in Lisbon.\n") ||
		!strings.Contains(out, "You wrote this memory, so this change waits for your OK.") {
		t.Errorf("show = %v %q\n%s", err, stderr, out)
	}

	out, stderr, seen, err = latere("", "apply", tidy, "mia")
	if err != nil || !strings.Contains(out, "The change is applied.") {
		t.Errorf("apply = %v %q\n%s", err, stderr, out)
	}
	if !slices.Equal(seen, []string{"GET /v1/account/runs/" + tidy, "POST /v1/account/runs/" + tidy + "/apply"}) {
		t.Errorf("apply asked %v", seen)
	}
	mu.Lock()
	if changes, _ := bodies["POST /v1/account/runs/"+tidy+"/apply"]["changes"].([]any); len(changes) != 1 || changes[0] != "mia" {
		t.Errorf("apply body = %v", bodies["POST /v1/account/runs/"+tidy+"/apply"])
	}
	mu.Unlock()

	exitOne := func(err error) bool {
		exit, ok := errors.AsType[*exec.ExitError](err)
		return ok && exit.ExitCode() == 1
	}
	_, stderr, seen, err = latere("", "undo", tidy)
	if !exitOne(err) || !strings.Contains(stderr, "Undo the 2 changes") || !strings.Contains(stderr, "nothing was undone") || slices.Contains(seen, "POST /v1/account/runs/"+tidy+"/undo") {
		t.Errorf("undo without a yes = %v, sent %v\n%s", err, seen, stderr)
	}
	out, stderr, _, err = latere("y\n", "undo", tidy)
	if err != nil || !strings.Contains(out, "The change is undone. 1 memory you changed since was left as it is.") ||
		!strings.Contains(out, "Left as it is: you changed it since") {
		t.Errorf("undo = %v %q\n%s", err, stderr, out)
	}

	out, stderr, _, err = latere("", "show", "ses_gone")
	if !exitOne(err) || out != "" || !strings.Contains(stderr, "run_not_found: You have no tidy ses_gone.") {
		t.Errorf("show of a tidy not there = %v stdout %q stderr %q", err, out, stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	if minted == 0 {
		t.Error("no token was minted from the saved login")
	}
}
