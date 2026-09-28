// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubPlatform is platformd's repository routes for the commands: the
// context's list, and a create that records what it was sent and answers
// what the case sets.
type stubPlatform struct {
	srv *httptest.Server

	mu      sync.Mutex
	seen    []string
	created map[string]string
	bearers []string
	// createStatus and createBody answer a create; a zero status answers
	// 201 with the created repository.
	createStatus int
	createBody   string
	list         string
}

func newStubPlatform(t *testing.T) *stubPlatform {
	t.Helper()
	p := &stubPlatform{list: `{"context":{"owner_type":"principal","owner_id":"p1","label":"alice","name":"alice","create":{"allowed":true,"remaining":98}},
"repositories":[{"id":"0a1b2c3d-0000-4000-8000-000000000001","owner_type":"principal","owner_id":"p1","owner_label":"alice","slug":"notes","visibility":"private","role":"admin","created_at":"2026-09-20T10:00:00Z"}],
"shared":[{"id":"0a1b2c3d-0000-4000-8000-000000000002","owner_type":"principal","owner_id":"p2","owner_label":"Bob","slug":"Site","visibility":"public","role":"writer","created_at":"2026-09-21T10:00:00Z"}]}`}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.URL.Path)
		p.bearers = append(p.bearers, r.Header.Get("Authorization"))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repositories":
			_, _ = io.WriteString(w, p.list)
		case r.Method == http.MethodPost && r.URL.Path == "/repositories":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("create body: %v", err)
			}
			p.mu.Lock()
			p.created = body
			status, answer := p.createStatus, p.createBody
			p.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, answer)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": body["id"], "owner_type": "principal", "owner_id": "p1",
				"owner_label": "alice", "slug": body["slug"], "visibility": body["visibility"]})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found","message":"No such route."}`)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// runRepos runs `latere repos …` against the stub with a bearer presented as
// given, and answers stdout and the command's error.
func runRepos(t *testing.T, p *stubPlatform, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LATERE_PLATFORM_TOKEN", "stub-bearer")
	t.Setenv("LATERE_PLATFORM_URL", p.srv.URL)
	t.Setenv("CODE_HOST", "")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	root := NewRoot("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"repos"}, args...))
	err := root.Execute()
	return out.String(), err
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestReposCreateSendsTheOneWritersBody(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		visibility string
	}{
		{"private by default", []string{"create", "alice/notes"}, "private"},
		{"public on request", []string{"create", "alice/notes", "--public"}, "public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newStubPlatform(t)
			out, err := runRepos(t, p, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if p.created["owner_label"] != "alice" || p.created["slug"] != "notes" || p.created["visibility"] != tc.visibility {
				t.Errorf("create body = %v", p.created)
			}
			if !uuidV4.MatchString(p.created["id"]) {
				t.Errorf("id = %q, want a lower-case version 4 UUID", p.created["id"])
			}
			if p.bearers[0] != "Bearer stub-bearer" {
				t.Errorf("bearer = %q", p.bearers[0])
			}
			for _, want := range []string{"alice/notes", p.created["id"], tc.visibility,
				"https://code.latere.ai/alice/notes.git", "git@code.latere.ai:alice/notes.git", "git push -u origin main"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestReposCreateChoosesAFreshIDEachTime(t *testing.T) {
	p := newStubPlatform(t)
	ids := map[string]bool{}
	for range 3 {
		if _, err := runRepos(t, p, "create", "alice/notes"); err != nil {
			t.Fatal(err)
		}
		ids[p.created["id"]] = true
	}
	if len(ids) != 3 {
		t.Errorf("ids = %v, want three distinct", ids)
	}
}

func TestReposCreateJSONIsTheServersAnswer(t *testing.T) {
	p := newStubPlatform(t)
	out, err := runRepos(t, p, "create", "alice/notes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got createdRepository
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got.ID != p.created["id"] || got.OwnerLabel != "alice" || got.Slug != "notes" || got.Visibility != "private" {
		t.Errorf("json = %+v", got)
	}
}

func TestReposCreateRefusesWhatIsNoAddress(t *testing.T) {
	p := newStubPlatform(t)
	for _, arg := range []string{"notes", "/notes", "alice/", "alice/notes/more", " "} {
		if _, err := runRepos(t, p, "create", arg); err == nil || !strings.Contains(err.Error(), "<owner>/<name>") {
			t.Errorf("create %q = %v", arg, err)
		}
	}
	if len(p.seen) != 0 {
		t.Errorf("requests = %v, want none", p.seen)
	}
}

// A refusal keeps the server's code, its sentence and its detail, in the
// section's own document and in the shared envelope alike.
func TestReposRefusalsCarryTheServersCodeAndSentence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
		hint       bool
	}{
		{
			name:   "section document",
			status: http.StatusForbidden,
			body:   `{"error":"forbidden","message":"That owner name is not yours to create under.","detail":"a repository lives under your handle"}`,
			want:   []string{"forbidden: That owner name is not yours to create under.", "detail: a repository lives under your handle"},
		},
		{
			name:   "taken",
			status: http.StatusConflict,
			body:   `{"error":"conflict","message":"That name is already taken under this owner."}`,
			want:   []string{"conflict: That name is already taken under this owner."},
		},
		{
			name:   "shared envelope",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"confirmation_required","message":"This create needs a person's confirmation.","details":{"action":"origo:repo.admin"}}}`,
			want:   []string{"confirmation_required: This create needs a person's confirmation."},
		},
		{
			name:   "an expired token",
			status: http.StatusUnauthorized,
			body:   `{"error":"unauthorized","message":"invalid token","detail":"token is expired"}`,
			want:   []string{"unauthorized: invalid token", "detail: token is expired", "latere login"},
			hint:   true,
		},
		{
			name:   "not json",
			status: http.StatusBadGateway,
			body:   `upstream unavailable`,
			want:   []string{"upstream unavailable"},
		},
		{
			name:   "empty",
			status: http.StatusServiceUnavailable,
			want:   []string{"HTTP 503"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newStubPlatform(t)
			p.createStatus, p.createBody = tc.status, tc.body
			_, err := runRepos(t, p, "create", "alice/notes")
			if err == nil {
				t.Fatal("want a refusal")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			pe, ok := errors.AsType[*platformError](err)
			if !ok || pe.Status != tc.status {
				t.Errorf("error = %#v, want a platformError with status %d", err, tc.status)
			}
			if !tc.hint && strings.Contains(err.Error(), "latere login") {
				t.Errorf("error %q carries the login hint", err)
			}
		})
	}
}

func TestReposListPrintsTheContextAndWhatIsShared(t *testing.T) {
	p := newStubPlatform(t)
	out, err := runRepos(t, p, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"REPOSITORY", "alice/notes", "private", "admin", "Shared with you:", "Bob/Site", "public", "writer"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if p.seen[0] != "GET /repositories" {
		t.Errorf("requests = %v", p.seen)
	}
}

func TestReposListOfAnEmptyContext(t *testing.T) {
	p := newStubPlatform(t)
	p.list = `{"context":{"owner_type":"org","owner_id":"o1","label":"acme","name":"Acme","create":{"allowed":false,"reason":"not_admin","remaining":0}},"repositories":null,"shared":null}`
	out, err := runRepos(t, p, "list")
	if err != nil || out != "No repositories in Acme.\n" {
		t.Errorf("list = %q, %v", out, err)
	}
	out, err = runRepos(t, p, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got reposContext
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Repositories == nil || got.Shared == nil || got.Context.Create.Reason != "not_admin" {
		t.Errorf("json = %+v, want empty lists and the context's standing", got)
	}
}

func TestReposListOfOnlySharedRepositories(t *testing.T) {
	p := newStubPlatform(t)
	p.list = `{"context":{"owner_type":"principal","owner_id":"p1","label":"","name":""},"repositories":[],
"shared":[{"id":"x","owner_label":"bob","slug":"site","visibility":"private","role":"reader","created_at":"2026-09-21T10:00:00Z"}]}`
	out, err := runRepos(t, p, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "No repositories in your personal account.\n\nShared with you:\n") || !strings.Contains(out, "bob/site") {
		t.Errorf("list =\n%s", out)
	}
}

func TestReposGetFindsByNameWithoutCase(t *testing.T) {
	p := newStubPlatform(t)
	out, err := runRepos(t, p, "get", "bob/site")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Bob/Site", "0a1b2c3d-0000-4000-8000-000000000002", "writer", "2026-09-21T10:00:00Z", "https://code.latere.ai/Bob/Site.git"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out, err = runRepos(t, p, "get", "ALICE/notes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got listedRepository
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.ID != "0a1b2c3d-0000-4000-8000-000000000001" || got.Role != "admin" {
		t.Errorf("json = %+v, %v", got, err)
	}
}

func TestReposGetOfARepositoryOutsideTheContext(t *testing.T) {
	p := newStubPlatform(t)
	_, err := runRepos(t, p, "get", "acme/site")
	if err == nil || !strings.Contains(err.Error(), "no repository acme/site in your current context") || !strings.Contains(err.Error(), "latere org") {
		t.Errorf("get = %v", err)
	}
}

func TestReposCloneURLsFollowTheCodeHost(t *testing.T) {
	t.Setenv("CODE_HOST", "localhost:8081")
	https, ssh := cloneURLs("alice", "notes")
	if https != "https://localhost:8081/alice/notes.git" || ssh != "git@localhost:8081:alice/notes.git" {
		t.Errorf("clone URLs = %q, %q", https, ssh)
	}
}

// Without LATERE_PLATFORM_TOKEN the commands present an actor token minted
// for the platform's audience from the saved login, never the login itself.
func TestReposMintsForThePlatformsAudience(t *testing.T) {
	p := newStubPlatform(t)
	var audience string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/actor-tokens" || r.Header.Get("Authorization") != "Bearer saved-login" {
			t.Errorf("auth request %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Audience string `json:"audience"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		audience = body.Audience
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"actor_token":"platform-actor","expires_in":300}`)
	}))
	defer auth.Close()
	writeAuthTokenFile(t, "saved-login", "", time.Now().Add(time.Hour))
	t.Setenv("AUTH_URL", auth.URL)
	t.Setenv("LATERE_PLATFORM_URL", p.srv.URL)
	t.Setenv("LATERE_PLATFORM_TOKEN", "")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	root := NewRoot("test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"repos", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if audience != platformAudience || p.bearers[0] != "Bearer platform-actor" {
		t.Errorf("audience = %q, bearer = %q", audience, p.bearers[0])
	}
}

func TestReposWithNoLoginSaysToSignIn(t *testing.T) {
	p := newStubPlatform(t)
	t.Setenv("LATERE_AUTH_TOKEN_FILE", t.TempDir()+"/absent.json")
	t.Setenv("LATERE_PLATFORM_URL", p.srv.URL)
	t.Setenv("LATERE_PLATFORM_TOKEN", "")
	root := NewRoot("test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"repos", "list"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cannot authenticate to the platform") {
		t.Errorf("list = %v", err)
	}
	if len(p.seen) != 0 {
		t.Errorf("requests = %v, want none", p.seen)
	}
}

func TestReposUnreachablePlatform(t *testing.T) {
	p := newStubPlatform(t)
	p.srv.Close()
	if _, err := runRepos(t, p, "list"); err == nil || !strings.Contains(err.Error(), "reach the platform at") {
		t.Errorf("list = %v", err)
	}
}

func TestReposOversizedAnswer(t *testing.T) {
	p := newStubPlatform(t)
	p.list = `{"context":{},"repositories":[],"shared":[],"pad":"` + strings.Repeat("x", maxPlatformResponse) + `"}`
	if _, err := runRepos(t, p, "list"); err == nil || !strings.Contains(err.Error(), "exceeds 4 MiB") {
		t.Errorf("list = %v", err)
	}
}

func TestReposUnparsableAnswer(t *testing.T) {
	p := newStubPlatform(t)
	p.list = `{"context":`
	if _, err := runRepos(t, p, "list"); err == nil || !strings.Contains(err.Error(), "parse the platform's answer") {
		t.Errorf("list = %v", err)
	}
}

func TestReposWithoutASubcommandShowsHelp(t *testing.T) {
	p := newStubPlatform(t)
	if _, err := runRepos(t, p); err != nil {
		t.Errorf("repos = %v", err)
	}
}

func TestReposPlatformURLDefaultAndOverride(t *testing.T) {
	t.Setenv("LATERE_PLATFORM_URL", "")
	if got := resolvePlatformURL(""); got != defaultPlatformURL {
		t.Errorf("default = %q", got)
	}
	t.Setenv("LATERE_PLATFORM_URL", "https://platform.example/")
	if got := resolvePlatformURL(""); got != "https://platform.example" {
		t.Errorf("environment = %q", got)
	}
	if got := resolvePlatformURL("http://localhost:8080/"); got != "http://localhost:8080" {
		t.Errorf("flag = %q", got)
	}
}
