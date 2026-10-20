// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"latere.ai/x/sdk-go/apps"
)

// The ids of the stub's two deploys of hello: the newest, a preview of
// main, and the deploy of the release v1.0.0 before it.
const (
	stubPreviewID = "5d2f8a1c-3b4e-4f6a-9c7d-8e1f2a3b4c5d"
	stubReleaseID = "0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e"
)

// stubAppID is the id of every app the stub answers, which the platform
// keeps the people invited under.
const stubAppID = "123e4567-e89b-42d3-a456-426614174000"

// stubAppJSON is an App as the API answers it, for slug: a person's own app
// that is public, as an app made before apps started private is.
func stubAppJSON(slug string) string {
	return stubAppAs(slug, "public", "personal")
}

// stubAppAs is the App of slug with the visibility and the kind of owner
// given.
func stubAppAs(slug, visibility, owner string) string {
	return fmt.Sprintf(`{"id":"123e4567-e89b-42d3-a456-426614174000","slug":%[1]q,"name":"Hello","owner":{"kind":%[3]q,"id":"u-1"},
"visibility":%[2]q,"state":"active","health":"ready","url":"https://%[1]s.latere.site","preview_url":"https://next--%[1]s.latere.site",
"current_deploy":null,"latest_preview":null,"settings":{"sleep_after_seconds":1800,"always_on":false,"previews":{"visibility":"inherit"}},
"repository":{"clone_url":"https://code.latere.ai/r/123e4567-e89b-42d3-a456-426614174000.git","push_url":"https://code.latere.ai/u-1/%[1]s.git"},
"cluster":{"dockerfile_builds":false},"env_pending":false,"pending_deploy":false,"slug_generated":false,
"created_at":"2026-09-22T12:00:00Z","updated_at":"2026-09-22T12:00:00Z"}`, slug, visibility, owner)
}

// stubAccessJSON is who can open the stub's app as the platform answers it:
// two people invited, one who accepted and one who has not yet, no password,
// and a platform that sets one.
func stubAccessJSON() string {
	return fmt.Sprintf(`{"app_id":%q,"people":[
{"id":"inv_01JA4S0000000000000000000A","role":"viewer","email":"ines@example.com","state":"active","person":{"display_name":"Ines Moreau","handle":"ines"},"invited_at":%q,"claimed_at":%q},
{"id":"inv_01JA4T0000000000000000000B","role":"viewer","email":"pilot@harbor.example","state":"pending","invited_at":%q}],
"organization":null,"password":null,"actions":["invite","revoke","set_password"],
"limits":{"max_people":50,"max_emails_per_invite":20,"min_password":8,"max_password":128}}`, stubAppID,
		time.Now().Add(-5*24*time.Hour).UTC().Format(time.RFC3339), time.Now().Add(-4*24*time.Hour).UTC().Format(time.RFC3339),
		time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339))
}

// stubPasswordSetAt is the moment the stub's app had its password set, when
// a case gives it one.
const stubPasswordSetAt = "2026-10-08T12:00:00Z"

// stubAccessWithPassword is stubAccessJSON for an app that has a password.
func stubAccessWithPassword() string {
	return strings.Replace(stubAccessJSON(), `"password":null`, `"password":{"set_at":"`+stubPasswordSetAt+`"}`, 1)
}

// stubAccessWithoutPasswords is stubAccessJSON from a platform that sets no
// app passwords: no set_password among the actions, and no password
// whatever the app has.
func stubAccessWithoutPasswords() string {
	return strings.Replace(stubAccessJSON(), `,"set_password"`, "", 1)
}

func stubDeployJSON(id, status, ref string, preview bool, components string) string {
	return fmt.Sprintf(`{"id":%q,"seq":3,"kind":"static","status":%q,"preview":%t,"commit_sha":"3f2a9c1b7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a",
"ref":%q,"client":"push","created_by":"https://auth.latere.ai|u-1","url":"https://hello.latere.site","preview_url":"https://%s--hello.latere.site",
"logs_url":"/v1/deploys/%s/logs","from_deploy":null,"components":%s,"error":null,"created_at":%q,
"started_at":null,"built_at":null,"ready_at":null,"live_at":null,"finished_at":null}`,
		id, status, preview, ref, id[:8], id, components, time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339))
}

const oneComponent = `[{"name":"app","kind":"static","route":"/","status":"built"}]`

// stubApps is the Apps API under its origin base path and auth's mint on one
// server. Every answer is a field a case may change.
type stubApps struct {
	srv *httptest.Server

	mu        sync.Mutex
	requests  []string
	bearers   []string
	audiences []string
	accepts   []string
	created   map[string]string

	createStatus int
	createBody   string
	// visibility is what the platform gives a create that names none, and
	// owner the kind of owner a created app has.
	visibility, owner string
	// patched is the body of the last change of an app; patchStatus and
	// patchBody refuse it. A change that names a slug moves the app to it,
	// as it was.
	patched     map[string]string
	patchStatus int
	patchBody   string

	// order is every request at either server as it arrived, each led by
	// the server it reached.
	order []string

	// platform is the platform on a server of its own, with the people
	// invited to the stub's app and its password under the app's id.
	platform         *httptest.Server
	platformRequests []string
	platformBearers  []string
	// passwords is the body of every set of the password, in order.
	// passwordStatus and passwordBody refuse a set, and passwordRemoveStatus
	// and passwordRemoveBody a removal.
	passwords            []map[string]string
	passwordStatus       int
	passwordBody         string
	passwordRemoveStatus int
	passwordRemoveBody   string
	// access answers the read of the people invited; accessStatus and
	// accessBody refuse it.
	access       string
	accessStatus int
	accessBody   string
	// invited is the body of the last invite. inviteStatus and inviteBody
	// answer it; a zero status answers 201 with one invitation for each
	// address.
	invited struct {
		Emails []string `json:"emails"`
		Role   string   `json:"role"`
	}
	inviteStatus int
	inviteBody   string
	// revokeStatus and revokeBody refuse a removal; revokeFailsOn limits the
	// refusal to that one invitation.
	revokeStatus  int
	revokeBody    string
	revokeFailsOn string

	// pages answer GET /apps in turn, the first for no cursor.
	pages    []string
	apps     map[string]string
	deploys  map[string]string
	releases map[string]string
	// logs answers the stored log; frames are written, each flushed, as the
	// followed log.
	logs      string
	logStatus int
	logBody   string
	frames    []string
	// frameDelay is the pause before each frame, as a building deploy's
	// stream has.
	frameDelay time.Duration
	// deploysSeq, when set, answers the deploys of every app in turn, the
	// last one again once they run out, as deploys a push creates appear.
	deploysSeq   []string
	deploysReads int
}

func newStubApps(t *testing.T) *stubApps {
	t.Helper()
	s := &stubApps{
		visibility: "private",
		owner:      "personal",
		access:     stubAccessJSON(),
		apps:       map[string]string{"hello": stubAppJSON("hello")},
		deploys: map[string]string{"hello": `{"deploys":[` +
			stubDeployJSON(stubPreviewID, "ready", "refs/heads/main", true, oneComponent) + `,` +
			stubDeployJSON(stubReleaseID, "live", "refs/tags/v1.0.0", false, oneComponent) + `]}`},
		releases: map[string]string{"hello": `{"releases":[
{"id":"r2","tag":"v1.1.0","commit_sha":"c2","status":"pending","reason":null,"source_deploy":null,"deploy":null,"via":"push","created_by":"x","created_at":"2026-09-22T12:06:00Z","released_at":null,"superseded_at":null},
{"id":"r1","tag":"v1.0.0","commit_sha":"c1","status":"released","reason":null,"source_deploy":"` + stubPreviewID + `","deploy":"` + stubReleaseID + `","via":"push","created_by":"x","created_at":"2026-09-22T12:05:00Z","released_at":"2026-09-22T12:05:03Z","superseded_at":null}]}`},
		logs: `{"seq":1,"ts":"2026-09-22T12:00:01Z","src":"fetch","component":"app","line":"fetching 3f2a9c1 for app"}
{"seq":2,"ts":"2026-09-22T12:00:04Z","src":"build","component":"app","line":"\u001b[32mcompiled\u001b[0m 14 files"}
`,
	}
	s.pages = []string{`{"items":[` + stubAppJSON("hello") + `],"has_more":false,"next_cursor":null}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /actor-tokens", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Audience string `json:"audience"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.audiences = append(s.audiences, body.Audience)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actor_token": fakeJWT(t, map[string]any{"sub": "u-1", "iss": audienceIssuer, "aud": body.Audience}),
			"expires_in":  300,
		})
	})
	mux.HandleFunc("POST /v1/apps/apps", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("create body: %v", err)
		}
		s.mu.Lock()
		s.created = body
		status, answer := s.createStatus, s.createBody
		visibility, owner := defaultStr(body["visibility"], s.visibility), s.owner
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, answer)
			return
		}
		slug := defaultStr(body["slug"], "brave-otter-2041")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, stubAppAs(slug, visibility, owner))
	})
	mux.HandleFunc("PATCH /v1/apps/apps/{slug}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("change body: %v", err)
		}
		slug := r.PathValue("slug")
		s.mu.Lock()
		s.patched = body
		status, answer := s.patchStatus, s.patchBody
		if to := body["slug"]; status == 0 && to != "" {
			var was apps.App
			if err := json.Unmarshal([]byte(s.apps[slug]), &was); err != nil {
				t.Errorf("the stub's app %s: %v", slug, err)
			}
			delete(s.apps, slug)
			s.apps[to] = stubAppAs(to, string(was.Visibility), s.owner)
			answer = s.apps[to]
		} else if status == 0 {
			s.apps[slug] = stubAppAs(slug, body["visibility"], s.owner)
			answer = s.apps[slug]
		}
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, answer)
	})
	mux.HandleFunc("GET /v1/apps/apps", func(w http.ResponseWriter, r *http.Request) {
		i := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			_, _ = fmt.Sscanf(c, "page-%d", &i)
		}
		_, _ = io.WriteString(w, s.pages[i])
	})
	notFound := func(w http.ResponseWriter, slug string) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"app_not_found","message":"There is no app with this slug in this account.","details":{"slug":%q,"hint":"Check the slug, or switch to the organization that owns the app with `+"`latere org`"+`."}}}`, slug)
	}
	byApp := func(table func() map[string]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			body, ok := table()[r.PathValue("slug")]
			s.mu.Unlock()
			if !ok {
				notFound(w, r.PathValue("slug"))
				return
			}
			_, _ = io.WriteString(w, body)
		}
	}
	mux.HandleFunc("GET /v1/apps/apps/{slug}", byApp(func() map[string]string { return s.apps }))
	deploys := byApp(func() map[string]string { return s.deploys })
	mux.HandleFunc("GET /v1/apps/apps/{slug}/deploys", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		seq, n := s.deploysSeq, s.deploysReads
		s.deploysReads++
		s.mu.Unlock()
		if len(seq) == 0 {
			deploys(w, r)
			return
		}
		_, _ = io.WriteString(w, seq[min(n, len(seq)-1)])
	})
	mux.HandleFunc("GET /v1/apps/apps/{slug}/releases", byApp(func() map[string]string { return s.releases }))
	mux.HandleFunc("DELETE /v1/apps/apps/{slug}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.apps[r.PathValue("slug")]; !ok {
			notFound(w, r.PathValue("slug"))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"state":"deleting"}`)
	})
	mux.HandleFunc("GET /v1/apps/apps/{slug}/deploys/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		if s.logStatus != 0 {
			w.WriteHeader(s.logStatus)
			_, _ = io.WriteString(w, s.logBody)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = io.WriteString(w, s.logs)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, f := range s.frames {
			time.Sleep(s.frameDelay)
			_, _ = io.WriteString(w, f)
			flusher.Flush()
		}
	})
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/actor-tokens" {
			s.mu.Lock()
			s.order = append(s.order, "apps "+r.Method+" "+r.URL.RequestURI())
			s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
			s.bearers = append(s.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			s.accepts = append(s.accepts, r.Header.Get("Accept"))
			s.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)

	access := http.NewServeMux()
	// refused writes the refusal a case set for a route, and reports whether
	// it did.
	refused := func(w http.ResponseWriter, status int, body string) bool {
		if status == 0 {
			return false
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return true
	}
	access.HandleFunc("GET /v1/account/apps/{app_id}/access", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		status, body, list := s.accessStatus, s.accessBody, s.access
		s.mu.Unlock()
		if !refused(w, status, body) {
			_, _ = io.WriteString(w, list)
		}
	})
	access.HandleFunc("POST /v1/account/apps/{app_id}/access/invites", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.invited.Emails, s.invited.Role = nil, ""
		if err := json.NewDecoder(r.Body).Decode(&s.invited); err != nil {
			t.Errorf("invite body: %v", err)
		}
		if refused(w, s.inviteStatus, s.inviteBody) {
			return
		}
		var rows []string
		for i, email := range s.invited.Emails {
			rows = append(rows, fmt.Sprintf(`{"id":"inv_01JA4V000000000000000000%02d","role":"viewer","email":%q,"state":"pending","invited_at":"2026-10-07T10:10:00Z"}`, i, strings.ToLower(email)))
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"people":[%s],"skipped":[]}`, strings.Join(rows, ","))
	})
	access.HandleFunc("PUT /v1/account/apps/{app_id}/access/password", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("password body: %v", err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.passwords = append(s.passwords, body)
		if !refused(w, s.passwordStatus, s.passwordBody) {
			w.WriteHeader(http.StatusNoContent)
		}
	})
	access.HandleFunc("DELETE /v1/account/apps/{app_id}/access/password", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !refused(w, s.passwordRemoveStatus, s.passwordRemoveBody) {
			w.WriteHeader(http.StatusNoContent)
		}
	})
	access.HandleFunc("DELETE /v1/account/apps/{app_id}/access/{access_id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		status, body, only := s.revokeStatus, s.revokeBody, s.revokeFailsOn
		s.mu.Unlock()
		if only != "" && only != r.PathValue("access_id") {
			status = 0
		}
		if !refused(w, status, body) {
			w.WriteHeader(http.StatusNoContent)
		}
	})
	s.platform = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.order = append(s.order, "platform "+r.Method+" "+r.URL.RequestURI())
		s.platformRequests = append(s.platformRequests, r.Method+" "+r.URL.RequestURI())
		s.platformBearers = append(s.platformBearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		access.ServeHTTP(w, r)
	}))
	t.Cleanup(s.platform.Close)
	return s
}

func (s *stubApps) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// seenAtThePlatform is the requests the platform received, in order.
func (s *stubApps) seenAtThePlatform() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.platformRequests...)
}

// inOrder is the requests both servers received, in the order they arrived.
func (s *stubApps) inOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// passwordSent is the one password the platform was sent, and fails the
// test unless exactly one set arrived, carrying the password and nothing
// else.
func (s *stubApps) passwordSent(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.passwords) != 1 || len(s.passwords[0]) != 1 {
		t.Fatalf("sets of the password = %d, want one that carries the password alone", len(s.passwords))
	}
	return s.passwords[0]["password"]
}

// runAppIn runs `latere apps …` in dir against the stub, with a saved login
// the stub's mint answers, and answers stdout, stderr and the error.
func runAppIn(t *testing.T, s *stubApps, dir, stdin string, args ...string) (string, string, error) {
	t.Helper()
	t.Chdir(dir)
	// git looks for a repository no further up than dir, so a case outside
	// one never finds the checkout the suite runs in.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	t.Setenv("LATERE_APPS_URL", s.srv.URL+"/v1/apps")
	t.Setenv("LATERE_APPS_TOKEN", "")
	t.Setenv("LATERE_PLATFORM_URL", s.platform.URL)
	t.Setenv("LATERE_PLATFORM_TOKEN", "")
	t.Setenv("AUTH_URL", s.srv.URL)
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	writeAuthTokenFile(t, "saved-login", "", time.Now().Add(time.Hour))
	root := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"apps"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// runApp runs `latere apps …` in an empty directory outside any repository.
func runApp(t *testing.T, s *stubApps, args ...string) (string, string, error) {
	t.Helper()
	return runAppIn(t, s, t.TempDir(), "", args...)
}

// gitRepo is a fresh repository under its own global configuration, so the
// developer's git configuration plays no part. It skips without git.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func wantContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

// ---- create ----

func TestAppCreateOutsideARepository(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "create", "Hello World", "--slug", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if s.created["name"] != "Hello World" || s.created["slug"] != "hello" || len(s.created) != 2 {
		t.Errorf("create body = %v, want the name and the slug and no visibility", s.created)
	}
	wantContains(t, out, "https://hello.latere.site", "https://next--hello.latere.site", "https://code.latere.ai/u-1/hello.git",
		"visibility: private\n\nOnly you and the people you invite can open it, each after signing in with a Latere account.\n"+
			"Open it to anyone who has its address, or invite people by email:\n  latere apps visibility unlisted hello\n  latere apps share hello <email>...\n",
		"not a git repository, so no remote was added",
		"Next:\n  git remote add latere https://code.latere.ai/u-1/hello.git && git push latere main\n")
	for i, aud := range s.audiences {
		if aud != appAudience {
			t.Errorf("minted audience %q, want %q", aud, appAudience)
		}
		if audienceOf(t, s.bearers[i]) != appAudience {
			t.Errorf("bearer %d is not for %q", i, appAudience)
		}
	}
	if len(s.audiences) != 1 {
		t.Errorf("mints = %v, want one", s.audiences)
	}
}

// A create that names nothing sends an empty body, so the platform decides
// the slug and who can open the app.
func TestAppCreateWithoutANameSendsAnEmptyBody(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "create")
	if err != nil {
		t.Fatal(err)
	}
	if s.created == nil || len(s.created) != 0 {
		t.Errorf("create body = %v, want an empty object", s.created)
	}
	wantContains(t, out, "brave-otter-2041", "visibility: private\n")
}

func TestAppCreateSendsTheVisibilityNamed(t *testing.T) {
	for _, tc := range []struct {
		visibility string
		want       []string
		share      bool
	}{
		{"unlisted", []string{"visibility: unlisted\n\nAnyone who has its address can open it without signing in, and it is kept out of search engines.\n"}, false},
		{"private", []string{"visibility: private\n\nOnly you and the people you invite can open it"}, true},
	} {
		t.Run(tc.visibility, func(t *testing.T) {
			s := newStubApps(t)
			// The platform would give another one, so the flag is what the
			// answer carries.
			s.visibility = "public"
			out, _, err := runApp(t, s, "create", "--slug", "hello", "--visibility", tc.visibility)
			if err != nil {
				t.Fatal(err)
			}
			if s.created["visibility"] != tc.visibility || len(s.created) != 2 {
				t.Errorf("create body = %v", s.created)
			}
			wantContains(t, out, tc.want...)
			if strings.Contains(out, "latere apps share") != tc.share {
				t.Errorf("the commands that open or share a private app are printed: %t, want %t:\n%s", !tc.share, tc.share, out)
			}
		})
	}
}

// The visibility printed is the one the answer carries, whatever a new app
// starts as: a platform that still creates a personal app public is read as
// it answers.
func TestAppCreatePrintsTheVisibilityOfTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, visibility, owner string
		want                    string
	}{
		{"public", "public", "personal", "visibility: public\n\nAnyone can open it, and search engines may list it.\n"},
		{"an organization's", "private", "organization", "visibility: private\n\nOnly the members of its organization and the people invited can open it, each after signing in with a Latere account.\n"},
		{"a visibility this version does not know", "members", "personal", "visibility: members\n\nThis directory is not a git repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.visibility, s.owner = tc.visibility, tc.owner
			out, _, err := runApp(t, s, "create", "--slug", "hello")
			if err != nil {
				t.Fatal(err)
			}
			if _, sent := s.created["visibility"]; sent {
				t.Errorf("create body = %v, want no visibility", s.created)
			}
			wantContains(t, out, tc.want)
			if (tc.visibility == "private") != strings.Contains(out, "latere apps share hello <email>...") {
				t.Errorf("output:\n%s", out)
			}
		})
	}
}

// Public is the platform's to give, and a value that is neither choice is a
// typing mistake: both are refused before any request.
func TestAppCreateRefusesAVisibilityAPersonCannotChoose(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []string
	}{
		{"public", []string{"public cannot be chosen here", "an app becomes public when it is shown in the gallery", "the app's Share in the chat", "reviewed first"}},
		{"publik", []string{`"publik" is not a visibility; choose private or unlisted`}},
		{"", []string{`"" is not a visibility; choose private or unlisted`}},
	} {
		t.Run(tc.value, func(t *testing.T) {
			s := newStubApps(t)
			out, _, err := runApp(t, s, "create", "--slug", "hello", "--visibility", tc.value)
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			if len(s.seen()) != 0 || out != "" {
				t.Errorf("requests = %v, stdout = %q, want neither", s.seen(), out)
			}
		})
	}
}

func TestAppCreateAddsTheRemote(t *testing.T) {
	for _, tc := range []struct {
		name, remote string
		args         []string
	}{
		{"default name", "latere", nil},
		{"named remote", "deploy", []string{"--remote", "deploy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			dir := gitRepo(t)
			out, _, err := runAppIn(t, s, dir, "", append([]string{"create", "--slug", "hello"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if got := gitRun(t, dir, "remote", "get-url", tc.remote); got != "https://code.latere.ai/u-1/hello.git" {
				t.Errorf("remote %s = %q", tc.remote, got)
			}
			wantContains(t, out, "Added the git remote "+tc.remote+".", "Next:\n  git push "+tc.remote+" main\n")
		})
	}
}

func TestAppCreateNoRemoteLeavesTheRepositoryAlone(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	out, _, err := runAppIn(t, s, dir, "", "create", "--slug", "hello", "--no-remote")
	if err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, dir, "remote"); got != "" {
		t.Errorf("remotes = %q, want none", got)
	}
	if strings.Contains(out, "remote was added") || strings.Contains(out, "Added the git remote") {
		t.Errorf("output speaks of a remote:\n%s", out)
	}
	wantContains(t, out, "git remote add latere https://code.latere.ai/u-1/hello.git && git push latere main")
}

// A remote of that name for another app refuses before any request, so no
// app is created that the repository does not push to.
func TestAppCreateRefusesARemoteOfAnotherApp(t *testing.T) {
	for _, args := range [][]string{{"create"}, {"create", "--slug", "hello"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newStubApps(t)
			dir := gitRepo(t)
			gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/other.git")
			_, _, err := runAppIn(t, s, dir, "", args...)
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), "already has a git remote named latere", "https://code.latere.ai/u-1/other.git", "nothing was created", "--remote <name>", "--no-remote")
			if len(s.seen()) != 0 {
				t.Errorf("requests = %v, want none", s.seen())
			}
			if got := gitRun(t, dir, "remote", "get-url", "latere"); got != "https://code.latere.ai/u-1/other.git" {
				t.Errorf("remote = %q, want it unchanged", got)
			}
		})
	}
}

// A remote that names the slug asked for is a deleted app's: the create
// restores it, and the remote already points at it.
func TestAppCreateRestoreKeepsTheSameRemote(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	out, _, err := runAppIn(t, s, dir, "", "create", "--slug", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "The git remote latere already points at the push URL.", "git push latere main")
}

// The same slug under another owner's path is not overwritten.
func TestAppCreateKeepsADifferentRemoteOfTheSameSlug(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/acme/hello.git")
	out, _, err := runAppIn(t, s, dir, "", "create", "--slug", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "points at https://code.latere.ai/acme/hello.git, not at this app's push URL; it was left as it is",
		"git push https://code.latere.ai/u-1/hello.git main")
	if got := gitRun(t, dir, "remote", "get-url", "latere"); got != "https://code.latere.ai/acme/hello.git" {
		t.Errorf("remote = %q, want it unchanged", got)
	}
}

func TestAppCreateJSONIsTheAPIsApp(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	out, errOut, err := runAppIn(t, s, dir, "", "create", "--slug", "hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got["slug"] != "hello" || got["cluster"] == nil {
		t.Errorf("json = %v", got)
	}
	wantContains(t, errOut, "Added the git remote latere.")
}

func TestAppCreateRefusals(t *testing.T) {
	envelope := func(code, message, details string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"details":%s}}`, code, message, details)
	}
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
	}{
		{"taken", envelope("slug_taken", "This slug is already in use.", `{"slug":"hello","hint":"Pick another slug or leave it out to get a generated one."}`), 409,
			[]string{"slug_taken: The slug hello is already in use.", "Pick another slug"}},
		{"held", envelope("slug_held", "This slug was released recently and is held for its previous owner.", `{"slug":"hello","released_at":"2026-10-01T09:00:00Z","hold_ends_at":"2026-10-08T09:00:00Z","hint":"Pick another slug or wait seven days."}`), 409,
			[]string{"slug_held: The slug hello was released recently and is held for its previous owner until 2026-10-08 09:00 UTC.", "wait seven days"}},
		{"held without its end", envelope("slug_held", "This slug was released recently and is held for its previous owner.", `{"slug":"hello","hint":"Pick another slug or wait seven days."}`), 409,
			[]string{"slug_held: The slug hello was released recently and is held for its previous owner.\n"}},
		{"reserved", envelope("slug_reserved", "This slug is reserved by the platform.", `{"slug":"hello","hint":"Pick another slug."}`), 409,
			[]string{"slug_reserved: The slug hello is reserved by the platform."}},
		{"length", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"length","hint":"Use 1 to 40 lowercase letters, digits, and single hyphens."}`), 400,
			[]string{"invalid_slug: The slug hello is not a valid address: a slug is 1 to 40 characters."}},
		{"charset", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"charset","hint":"h"}`), 400,
			[]string{"not a valid address: a slug takes only lowercase letters, digits and hyphens."}},
		{"boundary", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"boundary","hint":"h"}`), 400,
			[]string{"not a valid address: a slug starts and ends with a letter or a digit."}},
		{"double hyphen", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"double_hyphen","hint":"h"}`), 400,
			[]string{"not a valid address: a slug has no two hyphens in a row."}},
		{"short", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"short","hint":"h"}`), 400,
			[]string{"invalid_slug: The slug hello is not a valid address: a slug under three characters needs a plan that allows short slugs."}},
		{"unknown rule", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"new_rule","hint":"h"}`), 400,
			[]string{"invalid_slug: The slug hello is not a valid address.\nh"}},
		{"generation", envelope("slug_generation_failed", "The platform could not find a free address.", `{"attempts":5,"hint":"Try again."}`), 503,
			[]string{"slug_generation_failed: The platform could not find a free address in 5 tries."}},
		{"repository", envelope("repository_create_failed", "The app's repository could not be created.", `{"stage":"register","hint":"Try again."}`), 502,
			[]string{"repository_create_failed: The app's repository could not be created (at register), so no app was created."}},
		{"restore", envelope("repository_unavailable", "The app's repository did not answer.", `{"hint":"Try again in a few minutes."}`), 503,
			[]string{"repository_unavailable: The repository of the deleted app this slug restores did not answer"}},
		{"forbidden", envelope("forbidden", "You do not have permission to do this.", `{"action":"app.create","reason":"app_limit","hint":"Ask an owner."}`), 403,
			[]string{"forbidden: You may not create an app in your current context (app_limit).", "Ask an owner.", "`latere org`"}},
		{"visibility", envelope("invalid_request", "The request could not be read.", `{"field":"visibility","reason":"enum","hint":"h"}`), 400,
			[]string{"invalid_request: The platform refused the create's visibility."}},
		{"plain text", "json: unknown field \"owner\"", 400,
			[]string{"The platform could not read the create: json: unknown field \"owner\"."}},
		{"expired login", envelope("unauthenticated", "Sign in to continue.", `{"reason":"expired","hint":"Sign in again."}`), 401,
			[]string{"unauthenticated: Sign in to continue.", "Run `latere login` and try again."}},
		{"empty", "", 500, []string{"the Apps API answered HTTP 500"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.createStatus, s.createBody = tc.status, tc.body
			out, _, err := runApp(t, s, "create", "--slug", "hello")
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			var buf bytes.Buffer
			if code := HandleExitError(&buf, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

func TestAppAudienceMismatchKeepsTheAPIsHint(t *testing.T) {
	s := newStubApps(t)
	s.createStatus = 401
	s.createBody = `{"error":{"code":"audience_mismatch","message":"This token was issued for another service.","details":{"expected":["insula"],"hint":"Run the command again; the CLI mints a token for this API."}}}`
	_, _, err := runApp(t, s, "create")
	if err == nil || err.Error() != "audience_mismatch: This token was issued for another service.\nRun the command again; the CLI mints a token for this API." {
		t.Errorf("create = %v", err)
	}
}

// ---- list ----

func TestAppListShowsWhatProductionServes(t *testing.T) {
	s := newStubApps(t)
	s.apps["quiet"] = stubAppAs("quiet", "private", "personal")
	s.releases["quiet"] = `{"releases":[]}`
	s.apps["link"] = stubAppAs("link", "unlisted", "personal")
	s.releases["link"] = `{"releases":[]}`
	s.pages = []string{
		`{"items":[` + stubAppJSON("hello") + `],"has_more":true,"next_cursor":"page-1"}`,
		`{"items":[` + s.apps["quiet"] + `,` + s.apps["link"] + `],"has_more":false,"next_cursor":null}`,
	}
	out, _, err := runApp(t, s, "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || strings.Join(strings.Fields(lines[0]), " ") != "SLUG STATE VISIBILITY PRODUCTION ADDRESS" {
		t.Fatalf("list =\n%s", out)
	}
	if got := strings.Fields(lines[1]); strings.Join(got, " ") != "hello active public v1.0.0 https://hello.latere.site" {
		t.Errorf("hello row = %q", got)
	}
	if got := strings.Fields(lines[2]); strings.Join(got, " ") != "quiet active private - https://quiet.latere.site" {
		t.Errorf("quiet row = %q", got)
	}
	if got := strings.Fields(lines[3]); strings.Join(got, " ") != "link active unlisted - https://link.latere.site" {
		t.Errorf("link row = %q", got)
	}
	seen := s.seen()
	if seen[0] != "GET /v1/apps/apps?limit=200" || seen[1] != "GET /v1/apps/apps?cursor=page-1&limit=200" {
		t.Errorf("requests = %v", seen)
	}
	if len(s.audiences) != 1 {
		t.Errorf("mints = %v, want one for the command", s.audiences)
	}
}

func TestAppListJSONIsTheAPIsApps(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 || got[0]["slug"] != "hello" || got[0]["settings"] == nil {
		t.Errorf("json = %v %v", got, err)
	}
	for _, r := range s.seen() {
		if strings.Contains(r, "/releases") {
			t.Errorf("--json read releases: %v", s.seen())
		}
	}
}

func TestAppListOfAnEmptyContext(t *testing.T) {
	s := newStubApps(t)
	s.pages = []string{`{"items":[],"has_more":false,"next_cursor":null}`}
	out, _, err := runApp(t, s, "list")
	if err != nil || out != "No apps in your current context. Create one with: latere apps create\n" {
		t.Errorf("list = %q, %v", out, err)
	}
	out, _, err = runApp(t, s, "list", "--json")
	if err != nil || out != "[]\n" {
		t.Errorf("list --json = %q, %v", out, err)
	}
}

// The list ends at the page that names no next one, as the SDK pages every
// list, whatever else the page says.
func TestAppListEndsAtAPageWithoutANextCursor(t *testing.T) {
	s := newStubApps(t)
	s.pages = []string{`{"items":[` + stubAppJSON("hello") + `],"has_more":true,"next_cursor":null}`}
	out, _, err := runApp(t, s, "list")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "hello")
	if got := s.seen(); len(got) != 2 || !strings.HasPrefix(got[0], "GET /v1/apps/apps?") {
		t.Errorf("requests = %v, want one page and the release list", got)
	}
}

// ---- show ----

func TestAppShow(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "show", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out,
		"address:    https://hello.latere.site\n",
		"previews:   https://next--hello.latere.site\n",
		"production: v1.0.0, deploy 0b1c2d3e (live)\n",
		"preview:    5d2f8a1c of main, ready, https://5d2f8a1c--hello.latere.site\n",
		"push:       https://code.latere.ai/u-1/hello.git\n",
		"clone:      https://code.latere.ai/r/123e4567-e89b-42d3-a456-426614174000.git\n",
		"name:       Hello\n")
}

// Each visibility reads as its value and who that lets open the app, wrapped
// under its label, and a value this version does not know reads as it is.
func TestAppShowSaysWhoCanOpenTheApp(t *testing.T) {
	for _, tc := range []struct {
		visibility, owner, want string
	}{
		{"private", "personal", "visibility: private (only you and the people you invite can open it, each after signing in with a Latere account) health:"},
		{"private", "organization", "visibility: private (only the members of its organization and the people invited can open it, each after signing in with a Latere account) health:"},
		{"unlisted", "personal", "visibility: unlisted (anyone who has its address can open it without signing in, and it is kept out of search engines) health:"},
		{"public", "personal", "visibility: public (anyone can open it, and search engines may list it) health:"},
		{"members", "personal", "visibility: members health:"},
	} {
		t.Run(tc.visibility+" "+tc.owner, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", tc.visibility, tc.owner)
			out, _, err := runApp(t, s, "show", "hello")
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, oneLine(out), tc.want)
			for line := range strings.SplitSeq(out, "\n") {
				if len(line) > 88 {
					t.Errorf("a line is wider than a record's 88 columns: %q", line)
				}
			}
		})
	}
}

func TestAppShowOfAnAppWithNothingYet(t *testing.T) {
	s := newStubApps(t)
	s.deploys["hello"] = `{"deploys":[]}`
	s.releases["hello"] = `{"releases":[]}`
	out, _, err := runApp(t, s, "show", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "production: none\n", "preview:    none\n")
	out, _, err = runApp(t, s, "show", "hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil || string(got["release"]) != "null" || string(got["preview"]) != "null" || len(got) != 3 {
		t.Errorf("json = %s %v", out, err)
	}
}

func TestAppShowJSON(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "show", "hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		App     map[string]any `json:"app"`
		Release map[string]any `json:"release"`
		Preview map[string]any `json:"preview"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.App["slug"] != "hello" || got.Release["tag"] != "v1.0.0" || got.Preview["id"] != stubPreviewID {
		t.Errorf("json = %+v", got)
	}
}

func TestAppSlugFromTheRemote(t *testing.T) {
	for _, remote := range []string{"https://code.latere.ai/u-1/hello.git", "git@code.latere.ai:u-1/hello.git", "https://code.latere.ai/u-1/hello/"} {
		t.Run(remote, func(t *testing.T) {
			s := newStubApps(t)
			dir := gitRepo(t)
			gitRun(t, dir, "remote", "add", "latere", remote)
			out, _, err := runAppIn(t, s, dir, "", "show")
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, out, "app:        hello\n")
			if s.seen()[0] != "GET /v1/apps/apps/hello" {
				t.Errorf("requests = %v", s.seen())
			}
		})
	}
}

func TestAppSlugFromAStaleRemoteSaysWhereItCameFrom(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/gone.git")
	_, _, err := runAppIn(t, s, dir, "", "deploys")
	if err == nil {
		t.Fatal("want app_not_found")
	}
	wantContains(t, err.Error(), "app_not_found: There is no app with this slug in this account.", "`latere org`", "The slug gone was read from the git remote latere.")
}

func TestAppWithoutASlugOrARemote(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"show"}, "run `latere apps show <slug>`"},
		{[]string{"deploys"}, "run `latere apps deploys <slug>`"},
		{[]string{"logs"}, "run `latere apps logs <slug> [deploy]`"},
		{[]string{"access"}, "run `latere apps access <slug>`"},
		{[]string{"visibility", "unlisted"}, "run `latere apps visibility <private|unlisted> <slug>`"},
		{[]string{"rename", "hello-world"}, "run `latere apps rename <new-slug> <slug>`"},
		{[]string{"share", "ada@example.com"}, "run `latere apps share <slug> <email>...`"},
		{[]string{"unshare", "ada@example.com"}, "run `latere apps unshare <slug> <email>...`"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			s := newStubApps(t)
			_, _, err := runApp(t, s, tc.args...)
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), "missing the [slug] argument", "no git remote named latere", tc.want)
			if len(s.seen()) != 0 || len(s.seenAtThePlatform()) != 0 {
				t.Errorf("requests = %v and %v, want none", s.seen(), s.seenAtThePlatform())
			}
		})
	}
	t.Run("a repository without the remote", func(t *testing.T) {
		s := newStubApps(t)
		dir := gitRepo(t)
		gitRun(t, dir, "remote", "add", "origin", "https://example.com/hello.git")
		if _, _, err := runAppIn(t, s, dir, "", "show"); err == nil || !strings.Contains(err.Error(), "missing the [slug] argument") {
			t.Errorf("show = %v", err)
		}
	})
}

// ---- deploys ----

func TestAppDeploys(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "deploys", "hello")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "ID REF STATUS PREVIEW AGE" {
		t.Fatalf("deploys =\n%s", out)
	}
	if got := strings.Join(strings.Fields(lines[1]), " "); got != "5d2f8a1c main ready https://5d2f8a1c--hello.latere.site 3m" {
		t.Errorf("row = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[2]), " "); got != "0b1c2d3e v1.0.0 live https://0b1c2d3e--hello.latere.site 3m" {
		t.Errorf("row = %q", got)
	}
	out, _, err = runApp(t, s, "deploys", "hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 2 || got[1]["ref"] != "refs/tags/v1.0.0" {
		t.Errorf("json = %v %v", got, err)
	}
}

func TestAppDeploysOfAnAppWithNone(t *testing.T) {
	s := newStubApps(t)
	s.deploys["hello"] = `{"deploys":[]}`
	out, _, err := runApp(t, s, "deploys", "hello")
	if err != nil || out != "No deploys of hello yet. A push builds one: git push latere main\n" {
		t.Errorf("deploys = %q, %v", out, err)
	}
	out, _, err = runApp(t, s, "deploys", "hello", "--json")
	if err != nil || out != "[]\n" {
		t.Errorf("deploys --json = %q, %v", out, err)
	}
}

// ---- delete ----

func TestAppDelete(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		args        []string
		deletes     bool
	}{
		{"typed back", "hello\n", nil, true},
		{"typed back without a newline", "hello", nil, true},
		{"--yes", "", []string{"--yes"}, true},
		{"another slug", "hullo\n", nil, false},
		{"nothing typed", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			out, errOut, err := runAppIn(t, s, t.TempDir(), tc.stdin, append([]string{"delete", "hello"}, tc.args...)...)
			deleted := len(s.seen()) == 1 && s.seen()[0] == "DELETE /v1/apps/apps/hello"
			if deleted != tc.deletes {
				t.Fatalf("requests = %v, want a delete: %t", s.seen(), tc.deletes)
			}
			if !tc.deletes {
				if err == nil || !strings.Contains(err.Error(), "does not match hello; nothing was deleted") {
					t.Errorf("delete = %v", err)
				}
				if len(s.seen()) != 0 {
					t.Errorf("requests = %v", s.seen())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, out, "Deleting hello.", "held for your account for seven days", "latere apps create --slug hello")
			if (len(tc.args) == 0) != strings.Contains(errOut, "Type the slug to confirm") {
				t.Errorf("prompt = %q", errOut)
			}
		})
	}
}

func TestAppDeleteOfAnUnknownApp(t *testing.T) {
	s := newStubApps(t)
	if _, _, err := runApp(t, s, "delete", "gone", "--yes"); err == nil || !strings.Contains(err.Error(), "app_not_found") {
		t.Errorf("delete = %v", err)
	}
}

// ---- the connection ----

func TestAppTokenFromTheEnvironment(t *testing.T) {
	s := newStubApps(t)
	t.Setenv("LATERE_APPS_URL", s.srv.URL+"/v1/apps")
	t.Chdir(t.TempDir())
	t.Setenv("LATERE_APPS_TOKEN", "given-bearer")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "absent.json"))
	root := NewRoot("test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"apps", "deploys", "hello"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if s.bearers[0] != "given-bearer" || len(s.audiences) != 0 {
		t.Errorf("bearer = %q, mints = %v", s.bearers[0], s.audiences)
	}
}

func TestAppWithNoLoginSaysToSignIn(t *testing.T) {
	s := newStubApps(t)
	t.Setenv("LATERE_APPS_URL", s.srv.URL+"/v1/apps")
	t.Setenv("LATERE_APPS_TOKEN", "")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "absent.json"))
	root := NewRoot("test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"apps", "list"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cannot authenticate to Apps") {
		t.Errorf("list = %v", err)
	}
	if len(s.seen()) != 0 {
		t.Errorf("requests = %v, want none", s.seen())
	}
}

// An answer that is not the model it should be is said as one that could
// not be read, at either API, and nothing is printed for it.
func TestAppAnswersThatCannotBeRead(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = `{"slug":7}`
	out, _, err := runApp(t, s, "show", "hello")
	if err == nil || !strings.HasPrefix(err.Error(), "parse the Apps API's answer: json: cannot unmarshal number") || out != "" {
		t.Errorf("show = %q, %v", out, err)
	}
	s = newStubApps(t)
	s.access = `{"people":"none"}`
	out, _, err = runApp(t, s, "access", "hello")
	if err == nil || !strings.HasPrefix(err.Error(), "parse the platform's answer: json: cannot unmarshal string") || out != "" {
		t.Errorf("access = %q, %v", out, err)
	}
}

func TestAppUnreachableAPI(t *testing.T) {
	s := newStubApps(t)
	t.Setenv("LATERE_APPS_TOKEN", "given-bearer")
	t.Chdir(t.TempDir())
	root := NewRoot("test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"apps", "list", "--api-url", "http://127.0.0.1:1/v1/apps"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "reach the Apps API at http://127.0.0.1:1/v1/apps") {
		t.Errorf("list = %v", err)
	}
	if len(s.seen()) != 0 {
		t.Errorf("requests = %v", s.seen())
	}
}

func TestAppURLDefaultAndOverride(t *testing.T) {
	base := func(flagURL string) string {
		t.Helper()
		c, err := newAppClient(flagURL, "")
		if err != nil {
			t.Fatalf("newAppClient(%q) = %v", flagURL, err)
		}
		return c.base
	}
	t.Setenv("LATERE_APPS_URL", "")
	if got := base(""); got != defaultAppURL {
		t.Errorf("default = %q", got)
	}
	t.Setenv("LATERE_APPS_URL", "https://apps.example/v1/apps/")
	if got := base(""); got != "https://apps.example/v1/apps" {
		t.Errorf("environment = %q", got)
	}
	if got := base("http://localhost:8080/v1/apps"); got != "http://localhost:8080/v1/apps" {
		t.Errorf("flag = %q", got)
	}
	// The SDK writes the API's paths under the origin's /v1/apps, so an
	// address without it, such as the API's own root, cannot be reached.
	if _, err := newAppClient("http://localhost:8080/v1/", ""); err == nil || !strings.Contains(err.Error(), "ends in /v1/apps") {
		t.Errorf("a base URL without /v1/apps = %v", err)
	}
}

func TestAppWithoutASubcommandShowsHelp(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s)
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "git push latere main && latere apps logs -f", "create", "deploys", "logs",
		"A new app is private", "latere apps visibility unlisted", "latere apps share ada@example.com", "latere apps access",
		"latere apps password                 anyone with the password opens it", "latere apps password --generate",
		"so visibility, password, access,\nshare and unshare also call https://platform.latere.ai",
		"An app becomes public, which lets search engines list it, when it is shown\nin the gallery",
		"visibility  ", "access  ", "share  ", "unshare  ", "password  ")
	// Public is never offered as something to choose.
	for _, offer := range []string{"visibility public", "private|unlisted|public", "unlisted or public;"} {
		if strings.Contains(out, offer) {
			t.Errorf("the help offers public as a choice (%q):\n%s", offer, out)
		}
	}
}
