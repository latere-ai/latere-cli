// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// wantLacks fails for each phrase got holds.
func wantLacks(t *testing.T, got string, phrases ...string) {
	t.Helper()
	for _, phrase := range phrases {
		if strings.Contains(got, phrase) {
			t.Errorf("output has %q:\n%s", phrase, got)
		}
	}
}

// eachAPIItsOwnAudience checks that the Apps API received only bearers for
// its audience and the platform only bearers for the platform's, each minted
// from the saved login.
func eachAPIItsOwnAudience(t *testing.T, s *stubApps) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bearers) == 0 || len(s.platformBearers) == 0 {
		t.Fatalf("bearers at the Apps API = %d and at the platform = %d, want both reached", len(s.bearers), len(s.platformBearers))
	}
	for _, b := range s.bearers {
		if aud := audienceOf(t, b); aud != appAudience {
			t.Errorf("the Apps API received a bearer for %q, want %q", aud, appAudience)
		}
	}
	for _, b := range s.platformBearers {
		if aud := audienceOf(t, b); aud != platformAudience {
			t.Errorf("the platform received a bearer for %q, want %q", aud, platformAudience)
		}
	}
}

// stubAccessPath is the platform's path of who can open the stub's app.
const stubAccessPath = "/v1/account/apps/" + stubAppID + "/access"

// ---- visibility ----

func TestAppVisibilityChangesWhoCanOpenTheApp(t *testing.T) {
	for _, tc := range []struct {
		name, before, to, owner string
		want, lacks             []string
	}{
		{"a private app opened to its address", "private", "unlisted", "personal",
			[]string{"app:        hello\naddress:    https://hello.latere.site\nvisibility: unlisted\n\nAnyone with the link: it opens without signing in, and is kept out of search engines.\n"},
			[]string{"gallery", "latere apps share", "already"}},
		{"a public app leaves the gallery for its address", "public", "unlisted", "personal",
			[]string{"visibility: unlisted\n", "It was public before. If it was shown in the gallery, it has left the gallery.\n"},
			[]string{"latere apps share", "that request"}},
		{"a public app made private", "public", "private", "personal",
			[]string{"visibility: private\n\nOnly people invited: you and the people you invite open it, each after signing in with a Latere account.\n",
				"It was public before. If it was shown in the gallery, it has left the gallery.\n",
				"Invite people by email with: latere apps share hello <email>...\n"},
			nil},
		{"an unlisted app made private ends a request to be shown", "unlisted", "private", "organization",
			[]string{"Only people invited: the members of its organization and the people invited open it, each after signing in with a Latere account.\n",
				"If you had asked to show it in the gallery, that request has ended.\n", "latere apps share hello <email>..."},
			[]string{"It was public before"}},
		{"the visibility it already has", "unlisted", "unlisted", "personal",
			[]string{"visibility: unlisted\n", "It was unlisted already, so nothing changed.\n"},
			[]string{"gallery,", "that request"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.owner = tc.owner
			s.apps["hello"] = stubAppAs("hello", tc.before, tc.owner)
			out, _, err := runApp(t, s, "visibility", tc.to, "hello")
			if err != nil {
				t.Fatal(err)
			}
			if got := s.seen(); !slices.Equal(got, []string{"GET /v1/apps/apps/hello", "PATCH /v1/apps/apps/hello"}) {
				t.Errorf("requests = %v", got)
			}
			if len(s.patched) != 1 || s.patched["visibility"] != tc.to {
				t.Errorf("change body = %v, want the visibility alone", s.patched)
			}
			wantContains(t, out, tc.want...)
			wantLacks(t, out, append(tc.lacks, "password")...)
			// Whether the app has a password is read before the change, and
			// an app without one has nothing removed.
			if got := s.inOrder(); !slices.Equal(got, []string{"apps GET /v1/apps/apps/hello", "platform GET " + stubAccessPath, "apps PATCH /v1/apps/apps/hello"}) {
				t.Errorf("requests in order = %v", got)
			}
			eachAPIItsOwnAudience(t, s)
		})
	}
}

func TestAppVisibilityReadsTheSlugFromTheRemote(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	out, _, err := runAppIn(t, s, dir, "", "visibility", "private")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "app:        hello\n", "visibility: private\n")
	if s.patched["visibility"] != "private" {
		t.Errorf("change body = %v", s.patched)
	}
}

// Public is the platform's to give when it shows an app in the gallery, so
// naming it, like a value that is neither choice, makes no request.
func TestAppVisibilityRefusesWhatAPersonCannotChoose(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"visibility", "public", "hello"}, []string{"public cannot be chosen here: an app becomes public when it is shown in the gallery, which you ask for from the app's Share in the chat and which is reviewed first"}},
		{[]string{"visibility", "public"}, []string{"public cannot be chosen here"}},
		{[]string{"visibility", "hello"}, []string{`"hello" is not a visibility; choose private or unlisted`}},
		{[]string{"visibility", "hello", "unlisted"}, []string{`"hello" is not a visibility; choose private or unlisted`}},
		{[]string{"visibility"}, []string{"accepts between 1 and 2 arg(s)"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			s := newStubApps(t)
			out, _, err := runApp(t, s, tc.args...)
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

func TestAppVisibilityJSONIsTheAPIsApp(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "visibility", "unlisted", "hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got["visibility"] != "unlisted" || got["slug"] != "hello" || got["cluster"] == nil {
		t.Errorf("json = %v", got)
	}
}

func TestAppVisibilityRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
	}{
		{"forbidden", `{"error":{"code":"forbidden","message":"You do not have permission to do this.","details":{"action":"app.settings","reason":"not_owner","hint":"Ask an owner."}}}`, 403,
			[]string{"forbidden: You may not change who can open hello (not_owner).\nAsk an owner.\nThe app's owner can, and so can an owner or admin of the organization that owns it."}},
		{"forbidden without a reason", `{"error":{"code":"forbidden","message":"You do not have permission to do this.","details":{"action":"app.settings"}}}`, 403,
			[]string{"forbidden: You may not change who can open hello.\nThe app's owner can"}},
		{"refused value", `{"error":{"code":"invalid_request","message":"The request could not be read.","details":{"field":"visibility","reason":"enum","hint":"h"}}}`, 400,
			[]string{"invalid_request: The platform refused the change's visibility."}},
		{"being deleted", `{"error":{"code":"app_deleting","message":"This app is being deleted.","details":{"hint":"Wait for the deletion to finish."}}}`, 409,
			[]string{"app_deleting: This app is being deleted.\nWait for the deletion to finish."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.patchStatus, s.patchBody = tc.status, tc.body
			out, _, err := runApp(t, s, "visibility", "private", "hello")
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

func TestAppVisibilityOfAnUnknownApp(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/gone.git")
	_, _, err := runAppIn(t, s, dir, "", "visibility", "unlisted")
	if err == nil {
		t.Fatal("want app_not_found")
	}
	wantContains(t, err.Error(), "app_not_found", "The slug gone was read from the git remote latere.")
	if got := s.seen(); !slices.Equal(got, []string{"GET /v1/apps/apps/gone"}) {
		t.Errorf("requests = %v, want the read alone", got)
	}
}

// Neither value of a visibility is the choice a password makes, so both
// remove a password the app has and say what that changes.
func TestAppVisibilityRemovesAPassword(t *testing.T) {
	for _, tc := range []struct {
		name, before, to string
		want, lacks      []string
	}{
		{"anyone with the password to only people invited", "private", "private",
			[]string{"visibility: private\n\nOnly people invited: you and the people you invite open it, each after signing in with a Latere account.\n" +
				"It was private already.\n" +
				"Its password was removed. The people who used it no longer get in, and a visit made with it ends within a minute.\n" +
				"Invite people by email with: latere apps share hello <email>...\n"},
			[]string{"so nothing changed", "Anyone with the password"}},
		{"anyone with the password to anyone with the link", "private", "unlisted",
			[]string{"visibility: unlisted\n\nAnyone with the link: it opens without signing in, and is kept out of search engines.\n" +
				"Its password was removed. Anyone with the link opens the app, where a password decides nothing, and one left set would open the app again once it is private.\n"},
			[]string{"A password is set", "Remove it with", "no longer get in"}},
		{"a password left on an unlisted app, made private", "unlisted", "private",
			[]string{"Only people invited: you and the people you invite open it",
				"If you had asked to show it in the gallery, that request has ended.\n" +
					"Its password was removed. The people who used it no longer get in, and a visit made with it ends within a minute.\n"},
			[]string{"already"}},
		{"a password left on an unlisted app that stays unlisted", "unlisted", "unlisted",
			[]string{"It was unlisted already.\nIts password was removed. Anyone with the link opens the app"},
			[]string{"so nothing changed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", tc.before, "personal")
			s.access = stubAccessWithPassword()
			out, errOut, err := runApp(t, s, "visibility", tc.to, "hello")
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, out, tc.want...)
			wantLacks(t, out, tc.lacks...)
			if errOut != "" {
				t.Errorf("stderr = %q, want nothing", errOut)
			}
			if got := s.inOrder(); !slices.Equal(got, []string{"apps GET /v1/apps/apps/hello", "platform GET " + stubAccessPath,
				"apps PATCH /v1/apps/apps/hello", "platform DELETE " + stubAccessPath + "/password"}) {
				t.Errorf("requests in order = %v", got)
			}
			if len(s.patched) != 1 || s.patched["visibility"] != tc.to {
				t.Errorf("change body = %v, want the visibility alone", s.patched)
			}
			eachAPIItsOwnAudience(t, s)
		})
	}
}

// --json keeps standard output the app as the API answers it, so a password
// that went with the change is said on standard error.
func TestAppVisibilityJSONSaysARemovedPasswordOnStderr(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = stubAppAs("hello", "private", "personal")
	s.access = stubAccessWithPassword()
	out, errOut, err := runApp(t, s, "visibility", "unlisted", "hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got["visibility"] != "unlisted" || got["slug"] != "hello" {
		t.Errorf("json = %v", got)
	}
	if errOut != "Removed the password of hello. Anyone with the link opens the app, where a password decides nothing, and one left set would open the app again once it is private.\n" {
		t.Errorf("stderr = %q", errOut)
	}
	// An app without a password leaves standard error empty.
	s = newStubApps(t)
	if _, errOut, err := runApp(t, s, "visibility", "unlisted", "hello", "--json"); err != nil || errOut != "" {
		t.Errorf("without a password: stderr = %q, err = %v", errOut, err)
	}
}

// A platform that sets no passwords says of every app that it has none, and
// has no route to remove one, so the change is the visibility alone.
func TestAppVisibilityWhereThePlatformSetsNoPasswords(t *testing.T) {
	for _, access := range []string{stubAccessWithoutPasswords(),
		// The same platform answering a password all the same has none removed.
		strings.Replace(stubAccessWithPassword(), `,"set_password"`, "", 1),
		// A platform from before passwords names neither the password nor its bounds.
		`{"app_id":"` + stubAppID + `","people":[],"organization":null,"actions":["invite","revoke"],"limits":{"max_people":50,"max_emails_per_invite":20}}`} {
		s := newStubApps(t)
		s.access = access
		out, _, err := runApp(t, s, "visibility", "private", "hello")
		if err != nil {
			t.Fatal(err)
		}
		wantContains(t, out, "visibility: private\n")
		wantLacks(t, out, "password was removed")
		if got := s.seenAtThePlatform(); !slices.Equal(got, []string{"GET " + stubAccessPath}) {
			t.Errorf("requests at the platform = %v, want the read alone", got)
		}
	}
}

// Whether the app has a password is read before the change, so a platform
// that does not say leaves the app as it was.
func TestAppVisibilityChangesNothingWithoutThePlatformsAnswer(t *testing.T) {
	s := newStubApps(t)
	s.accessStatus = 503
	s.accessBody = `{"error":{"code":"apps_unavailable","message":"Sharing could not be reached. Try again shortly.","details":{"detail":"read the access list failed"}}}`
	out, _, err := runApp(t, s, "visibility", "unlisted", "hello")
	if err == nil {
		t.Fatal("want the failure")
	}
	wantContains(t, err.Error(), "hello was left as it is, since whether it has a password could not be read: apps_unavailable: Sharing could not be reached. Try again shortly.\ndetail: read the access list failed")
	if out != "" || s.patched != nil {
		t.Errorf("stdout = %q, change body = %v, want neither", out, s.patched)
	}
	if got := s.inOrder(); !slices.Equal(got, []string{"apps GET /v1/apps/apps/hello", "platform GET " + stubAccessPath}) {
		t.Errorf("requests in order = %v, want the two reads alone", got)
	}
	if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

// A password that could not be removed is still the app's: the command says
// what the change left, with the password, and then fails.
func TestAppVisibilitySaysAPasswordItCouldNotRemove(t *testing.T) {
	for _, jsonF := range []bool{false, true} {
		s := newStubApps(t)
		s.apps["hello"] = stubAppAs("hello", "unlisted", "personal")
		s.access = stubAccessWithPassword()
		s.passwordRemoveStatus = 503
		s.passwordRemoveBody = `{"error":{"code":"apps_unavailable","message":"Sharing could not be reached. Try again shortly.","details":{"detail":"remove the password failed"}}}`
		args := []string{"visibility", "private", "hello"}
		if jsonF {
			args = append(args, "--json")
		}
		out, errOut, err := runApp(t, s, args...)
		if err == nil {
			t.Fatal("want the failure")
		}
		wantContains(t, err.Error(), "hello is private now, and its password is still set: removing it failed: apps_unavailable: Sharing could not be reached. Try again shortly.\ndetail: remove the password failed")
		if jsonF {
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil || got["visibility"] != "private" {
				t.Errorf("json = %q (%v)", out, err)
			}
			if errOut != "" {
				t.Errorf("stderr = %q, want nothing of a removal", errOut)
			}
		} else {
			wantContains(t, out, "visibility: private\n\nAnyone with the password, set 2026-10-08 12:00 UTC: whoever types it at the app's address opens the app, with no Latere account.\n")
			wantLacks(t, out, "password was removed", "Only people invited")
		}
		if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
	}
}

// The platform keeps a password under the app's id, so an answer without one
// changes nothing.
func TestAppVisibilityOfAnAppWithoutAnID(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = `{"slug":"hello","visibility":"private"}`
	_, _, err := runApp(t, s, "visibility", "unlisted", "hello")
	if err == nil || !strings.Contains(err.Error(), "names no id for hello") {
		t.Errorf("visibility = %v", err)
	}
	if s.patched != nil || len(s.seenAtThePlatform()) != 0 {
		t.Errorf("change body = %v, requests at the platform = %v, want neither", s.patched, s.seenAtThePlatform())
	}
}

// ---- access ----

func TestAppAccessListsThePeopleInvited(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = stubAppAs("hello", "private", "personal")
	out, _, err := runApp(t, s, "access", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "app:        hello\naddress:    https://hello.latere.site\nvisibility: private\n\n"+
		"Only people invited: you and the people you invite open it, each after signing in with a Latere account.\n\n")
	wantLacks(t, out, "once it is private")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	table := lines[len(lines)-3:]
	if got := strings.Join(strings.Fields(table[0]), " "); got != "EMAIL STATE NAME INVITED" {
		t.Fatalf("header = %q in\n%s", got, out)
	}
	if got := strings.Join(strings.Fields(table[1]), " "); got != "ines@example.com active Ines Moreau 5d" {
		t.Errorf("row = %q", got)
	}
	if got := strings.Join(strings.Fields(table[2]), " "); got != "pilot@harbor.example pending - 3h" {
		t.Errorf("row = %q", got)
	}
	if got := s.seen(); !slices.Equal(got, []string{"GET /v1/apps/apps/hello"}) {
		t.Errorf("requests at the Apps API = %v", got)
	}
	if got := s.seenAtThePlatform(); !slices.Equal(got, []string{"GET " + stubAccessPath}) {
		t.Errorf("requests at the platform = %v", got)
	}
	eachAPIItsOwnAudience(t, s)
	if !slices.Equal(s.audiences, []string{appAudience, platformAudience}) {
		t.Errorf("mints = %v, want one for each API", s.audiences)
	}
}

func TestAppAccessByVisibility(t *testing.T) {
	const nobody = `{"app_id":"` + stubAppID + `","people":[],"organization":null,"actions":["invite","revoke"],"limits":{"max_people":50,"max_emails_per_invite":20}}`
	for _, tc := range []struct {
		name, visibility, owner, access string
		want, lacks                     []string
	}{
		{"private with no one invited", "private", "personal", nobody,
			[]string{"visibility: private\n", "Only people invited: you and the people you invite open it", "\nNo one is invited. Invite people by email with: latere apps share hello <email>...\n"},
			[]string{"EMAIL", "password"}},
		{"an organization's", "private", "organization", "",
			[]string{"Only people invited: the members of its organization and the people invited open it, each after signing in with a Latere account.\n", "ines@example.com"},
			[]string{"once it is private", "password"}},
		{"unlisted with people invited", "unlisted", "personal", "",
			[]string{"visibility: unlisted\n\nAnyone with the link: it opens without signing in, and is kept out of search engines.\nThe people invited open it once it is private.\n\nEMAIL"},
			[]string{"password"}},
		{"public", "public", "personal", "",
			[]string{"visibility: public\n\nAnyone with the link: it opens without signing in, and search engines may list it.\nThe people invited open it once it is private.\n\nEMAIL", "pilot@harbor.example"},
			[]string{"password"}},
		{"public with no one invited", "public", "personal", nobody,
			[]string{"Anyone with the link: it opens without signing in, and search engines may list it.\n\nNo one is invited."},
			[]string{"once it is private"}},
		{"private with a password", "private", "personal", stubAccessWithPassword(),
			[]string{"visibility: private\n\nAnyone with the password, set 2026-10-08 12:00 UTC: whoever types it at the app's address opens the app, with no Latere account.\n" +
				"You and the people you invite still open the app by signing in with a Latere account.\n\nEMAIL"},
			[]string{"Only people invited", "Anyone with the link", "Remove it with"}},
		{"an organization's with a password", "private", "organization", stubAccessWithPassword(),
			[]string{"Anyone with the password, set 2026-10-08 12:00 UTC: whoever types it",
				"The members of its organization and the people invited still open the app by signing in with a Latere account.\n"},
			nil},
		{"unlisted with a password left on it", "unlisted", "personal", stubAccessWithPassword(),
			[]string{"Anyone with the link: it opens without signing in, and is kept out of search engines.\n" +
				"A password is set since 2026-10-08 12:00 UTC. It decides nothing while anyone with the link opens the app, and opens the app again once it is private.\n" +
				"Remove it with: latere apps password --remove hello\nThe people invited open it once it is private.\n\nEMAIL"},
			[]string{"Anyone with the password"}},
		{"a platform that sets no passwords", "private", "personal", stubAccessWithoutPasswords(),
			[]string{"Only people invited: you and the people you invite open it"},
			[]string{"password"}},
		{"an accepted invitation named by the handle alone", "private", "personal",
			`{"people":[{"id":"inv_1","email":"ines@example.com","state":"active","person":{"display_name":"","handle":"ines"},"invited_at":"2026-10-06T08:00:00Z"}]}`,
			[]string{"active  ines  "}, nil},
		{"an accepted invitation whose name is unavailable", "private", "personal",
			`{"people":[{"id":"inv_1","email":"ines@example.com","state":"active","invited_at":"2026-10-06T08:00:00Z"}]}`,
			[]string{"active  -  "}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", tc.visibility, tc.owner)
			if tc.access != "" {
				s.access = tc.access
			}
			out, _, err := runApp(t, s, "access", "hello")
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, out, tc.want...)
			wantLacks(t, out, tc.lacks...)
		})
	}
}

func TestAppAccessJSONIsTheAppAndItsAccessList(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	out, _, err := runAppIn(t, s, dir, "", "access", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		App    map[string]any `json:"app"`
		Access struct {
			AppID  string           `json:"app_id"`
			People []map[string]any `json:"people"`
			Limits map[string]any   `json:"limits"`
		} `json:"access"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got.App["visibility"] != "public" || got.App["cluster"] == nil {
		t.Errorf("app = %v", got.App)
	}
	if got.Access.AppID != stubAppID || len(got.Access.People) != 2 || got.Access.People[0]["claimed_at"] == nil || got.Access.Limits["max_people"] != float64(50) {
		t.Errorf("access = %+v", got.Access)
	}
}

// Whether a password is set reaches a program as the platform answers it:
// the moment, or null, with the bounds of a password and the action that
// says the platform sets one.
func TestAppAccessJSONSaysWhetherAPasswordIsSet(t *testing.T) {
	for _, tc := range []struct {
		name, access string
		want         any
	}{
		{"set", stubAccessWithPassword(), map[string]any{"set_at": stubPasswordSetAt}},
		{"none", stubAccessJSON(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.access = tc.access
			out, _, err := runApp(t, s, "access", "hello", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Access struct {
					Password any            `json:"password"`
					Actions  []string       `json:"actions"`
					Limits   map[string]any `json:"limits"`
				} `json:"access"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("%v: %q", err, out)
			}
			if !reflect.DeepEqual(got.Access.Password, tc.want) {
				t.Errorf("password = %v, want %v", got.Access.Password, tc.want)
			}
			if !slices.Contains(got.Access.Actions, "set_password") || got.Access.Limits["min_password"] != float64(8) || got.Access.Limits["max_password"] != float64(128) {
				t.Errorf("access = %+v", got.Access)
			}
		})
	}
}

func TestAppAccessRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
	}{
		{"not one who may share", `{"error":{"code":"role_insufficient","message":"Only the people who can change this app's settings can share it.","details":{"detail":"app.settings on the app is refused with reason not_owner, and sharing an app is the right to change its visibility","reason":"not_owner"}}}`, 403,
			[]string{"role_insufficient: Only the people who can change this app's settings can share it.\ndetail: app.settings on the app is refused with reason not_owner"}},
		{"no such app at the platform", `{"error":{"code":"access_not_found","message":"We couldn't find that app.","details":{"app_id":"` + stubAppID + `","detail":"no app of this id is registered in the token's context"}}}`, 404,
			[]string{"access_not_found: The platform found no app hello in your current context.\ndetail: no app of this id is registered in the token's context"}},
		{"expired login", `{"error":{"code":"unauthorized","message":"Sign in to continue."}}`, 401,
			[]string{"unauthorized: Sign in to continue.", "Run `latere login` and try again"}},
		{"sharing down", `{"error":{"code":"apps_unavailable","message":"Sharing could not be reached. Try again shortly.","details":{"detail":"read the access list failed"}}}`, 503,
			[]string{"apps_unavailable: Sharing could not be reached. Try again shortly.\ndetail: read the access list failed"}},
		{"empty", "", 500, []string{"the platform answered HTTP 500"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.accessStatus, s.accessBody = tc.status, tc.body
			out, _, err := runApp(t, s, "access", "hello")
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

func TestAppAccessOfAnUnknownApp(t *testing.T) {
	s := newStubApps(t)
	if _, _, err := runApp(t, s, "access", "gone"); err == nil || !strings.Contains(err.Error(), "app_not_found") {
		t.Errorf("access = %v", err)
	}
	if len(s.seenAtThePlatform()) != 0 {
		t.Errorf("requests at the platform = %v, want none", s.seenAtThePlatform())
	}
}

// The people invited are kept under the app's id, so an answer without one
// reaches no list.
func TestAppAccessOfAnAppWithoutAnID(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = `{"slug":"hello","visibility":"private"}`
	_, _, err := runApp(t, s, "access", "hello")
	if err == nil || !strings.Contains(err.Error(), "names no id for hello") {
		t.Errorf("access = %v", err)
	}
	if len(s.seenAtThePlatform()) != 0 {
		t.Errorf("requests at the platform = %v, want none", s.seenAtThePlatform())
	}
}

// The platform's address and bearer are the ones the repository commands
// take: the flag over the variable, and a bearer presented as given.
func TestAppAccessPlatformAddressAndBearer(t *testing.T) {
	s := newStubApps(t)
	t.Setenv("LATERE_APPS_URL", s.srv.URL+"/v1/apps")
	t.Setenv("LATERE_APPS_TOKEN", "given-apps-bearer")
	t.Setenv("LATERE_PLATFORM_URL", "http://127.0.0.1:1")
	t.Setenv("LATERE_PLATFORM_TOKEN", "given-platform-bearer")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	t.Chdir(t.TempDir())
	run := func(args ...string) error {
		root := NewRoot("test")
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(append([]string{"apps"}, args...))
		return root.Execute()
	}
	if err := run("access", "hello"); err == nil || !strings.Contains(err.Error(), "reach the platform at http://127.0.0.1:1") {
		t.Errorf("access at the variable's address = %v", err)
	}
	if err := run("access", "hello", "--platform-url", s.platform.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if got := s.platformBearers; len(got) != 1 || got[0] != "given-platform-bearer" {
		t.Errorf("bearers at the platform = %v", got)
	}
	if len(s.audiences) != 0 {
		t.Errorf("mints = %v, want none with both bearers given", s.audiences)
	}
}

// ---- share ----

func TestAppShareInvitesByEmail(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = stubAppAs("hello", "private", "personal")
	out, _, err := runApp(t, s, "share", "hello", "Pilot@Harbor.example", " ada@example.com ")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.invited.Emails, []string{"Pilot@Harbor.example", "ada@example.com"}) || s.invited.Role != "viewer" {
		t.Errorf("invite body = %+v", s.invited)
	}
	if out != "Invited to hello:\n  pilot@harbor.example\n  ada@example.com\n\n"+
		"Invitations are not emailed. Send the people you invited the app's address:\n  https://hello.latere.site\n"+
		"Each of them opens it after signing in with a Latere account whose verified email is the address you invited.\n" {
		t.Errorf("share =\n%s", out)
	}
	if got := s.seen(); !slices.Equal(got, []string{"GET /v1/apps/apps/hello"}) {
		t.Errorf("requests at the Apps API = %v", got)
	}
	if got := s.seenAtThePlatform(); !slices.Equal(got, []string{"POST /v1/account/apps/" + stubAppID + "/access/invites"}) {
		t.Errorf("requests at the platform = %v", got)
	}
	eachAPIItsOwnAudience(t, s)
}

// An address in first position means the slug was left out, and it is read
// from the remote.
func TestAppShareReadsTheSlugFromTheRemote(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = stubAppAs("hello", "private", "personal")
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	out, _, err := runAppIn(t, s, dir, "", "share", "ada@example.com", "grace@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.invited.Emails, []string{"ada@example.com", "grace@example.com"}) {
		t.Errorf("invite body = %+v", s.invited)
	}
	wantContains(t, out, "Invited to hello:\n  ada@example.com\n  grace@example.com\n")
}

func TestAppShareSaysWhoWasSkippedAndWhy(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		status       int
		want, lacks  []string
	}{
		{"some skipped", `{"people":[{"id":"inv_1","role":"viewer","email":"ada@example.com","state":"pending","invited_at":"2026-10-07T10:10:00Z"}],
"skipped":[{"email":"me@example.com","reason":"self"},{"email":"ines@example.com","reason":"listed"},{"email":"x@example.com","reason":"blocked"}]}`, 201,
			[]string{"Invited to hello:\n  ada@example.com\nSkipped:\n",
				"  me@example.com    your own address, and you open the app already\n",
				"  ines@example.com  already invited\n",
				"  x@example.com     blocked\n",
				"Invitations are not emailed."},
			nil},
		{"every address skipped", `{"people":[],"skipped":[{"email":"ines@example.com","reason":"listed"}]}`, 200,
			[]string{"No one new was invited to hello.\nSkipped:\n  ines@example.com  already invited\n"},
			[]string{"Invitations are not emailed", "Invited to hello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", "private", "personal")
			s.inviteStatus, s.inviteBody = tc.status, tc.answer
			out, _, err := runApp(t, s, "share", "hello", "ada@example.com", "me@example.com", "ines@example.com", "x@example.com")
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, out, tc.want...)
			wantLacks(t, out, tc.lacks...)
		})
	}
}

// An app anyone can open needs no invitation, so the command says what the
// invitations it made are for.
func TestAppShareOfAnAppAnyoneCanOpen(t *testing.T) {
	for _, visibility := range []string{"unlisted", "public"} {
		t.Run(visibility, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", visibility, "personal")
			out, _, err := runApp(t, s, "share", "hello", "ada@example.com")
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, out, "Invited to hello:\n  ada@example.com\n", "Invitations are not emailed.", "https://hello.latere.site",
				"\nThe app is "+visibility+", so it is already open to anyone who has its address. The invitations decide who opens it once it is private.\n")
		})
	}
	t.Run("private", func(t *testing.T) {
		s := newStubApps(t)
		s.apps["hello"] = stubAppAs("hello", "private", "personal")
		out, _, err := runApp(t, s, "share", "hello", "ada@example.com")
		if err != nil {
			t.Fatal(err)
		}
		wantLacks(t, out, "already open", "once it is private")
	})
}

func TestAppShareJSONIsThePlatformsAnswer(t *testing.T) {
	s := newStubApps(t)
	out, errOut, err := runApp(t, s, "share", "hello", "ada@example.com", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		People  []map[string]any `json:"people"`
		Skipped []map[string]any `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if len(got.People) != 1 || got.People[0]["email"] != "ada@example.com" || got.People[0]["role"] != "viewer" || got.Skipped == nil {
		t.Errorf("json = %+v", got)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing", errOut)
	}
}

// What cannot be an address at all is refused before any request, and so is
// an invite that names no one.
func TestAppShareAndUnshareArguments(t *testing.T) {
	for _, verb := range []string{"share", "unshare"} {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"hello"}, "name at least one email address: latere apps " + verb + " [slug] <email>..."},
			{[]string{"hello", "ada"}, `"ada" is not an email address, and the slug comes first: latere apps ` + verb + " [slug] <email>..."},
			{[]string{"ada@example.com", "hello"}, `"hello" is not an email address, and the slug comes first`},
			{nil, "requires at least 1 arg(s)"},
		} {
			t.Run(verb+" "+strings.Join(tc.args, " "), func(t *testing.T) {
				s := newStubApps(t)
				out, _, err := runApp(t, s, append([]string{verb}, tc.args...)...)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s = %v, want %q", verb, err, tc.want)
				}
				if len(s.seen()) != 0 || len(s.seenAtThePlatform()) != 0 || out != "" {
					t.Errorf("requests = %v and %v, stdout = %q, want none", s.seen(), s.seenAtThePlatform(), out)
				}
			})
		}
	}
}

// The daily bound's refusal says when the person can invite again: now plus
// the wait the refusal names.
func TestAppShareSaysWhenTheDailyBoundEnds(t *testing.T) {
	s := newStubApps(t)
	s.inviteStatus = 429
	s.inviteBody = `{"error":{"code":"invite_limit","message":"You can't invite more people right now.","details":{"limit":"per_day","max":100,"retry_after":7200}}}`
	before := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Minute)
	_, _, err := runApp(t, s, "share", "hello", "a@example.com")
	after := time.Now().Add(2 * time.Hour).UTC()
	if err == nil {
		t.Fatal("want a refusal")
	}
	_, rest, ok := strings.Cut(err.Error(), "You can invite again from ")
	if !ok {
		t.Fatalf("share = %v", err)
	}
	at, parseErr := time.Parse("2006-01-02 15:04 UTC", strings.TrimSuffix(rest, "."))
	if parseErr != nil || at.Before(before) || at.After(after) {
		t.Errorf("invite again from %q (%v), want between %s and %s", rest, parseErr, before, after)
	}
}

func TestAppShareRefusals(t *testing.T) {
	envelope := func(code, message, details string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"details":%s}}`, code, message, details)
	}
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
	}{
		{"not an address", envelope("invalid_field", "The request could not be read.", `{"field":"emails","index":1,"detail":"emails[1] is not an email address"}`), 400,
			[]string{"invalid_field: b@ is not an email address. No one was invited.\ndetail: emails[1] is not an email address"}},
		{"too many addresses", envelope("invalid_field", "The request could not be read.", `{"field":"emails","detail":"emails names 1 to 20 addresses"}`), 400,
			[]string{"invalid_field: The platform refused the list of addresses. No one was invited.\ndetail: emails names 1 to 20 addresses"}},
		{"an index past the addresses sent", envelope("invalid_field", "The request could not be read.", `{"field":"emails","index":9,"detail":"emails[9] is not an email address"}`), 400,
			[]string{"invalid_field: The platform refused the list of addresses. No one was invited."}},
		{"another field", envelope("invalid_field", "The request could not be read.", `{"field":"role","detail":"role is \"viewer\""}`), 400,
			[]string{"invalid_field: The request could not be read.\ndetail: role is \"viewer\""}},
		{"the app is full", envelope("invite_limit", "You can't invite more people right now.", `{"limit":"people","max":50,"detail":"an app has at most 50 people with access, pending invites included"}`), 429,
			[]string{"invite_limit: These invitations would give hello more people invited than an app can have (50). No one was invited. `latere apps unshare` takes an invitation back.",
				"detail: an app has at most 50 people with access, pending invites included"}},
		{"the day is full", envelope("invite_limit", "You can't invite more people right now.", `{"limit":"per_day","max":100,"retry_after":3600,"detail":"a person makes at most 100 invites in 24 hours, across their apps"}`), 429,
			[]string{"invite_limit: You have made as many invitations as one person can for now (100). No one was invited. You can invite again from 20",
				" UTC.\ndetail: a person makes at most 100 invites in 24 hours, across their apps"}},
		{"a bound this version does not know", envelope("invite_limit", "You can't invite more people right now.", `{"limit":"per_app_day"}`), 429,
			[]string{"invite_limit: You can't invite more people right now."}},
		{"not one who may share", envelope("role_insufficient", "Only the people who can change this app's settings can share it.", `{"reason":"not_owner"}`), 403,
			[]string{"role_insufficient: Only the people who can change this app's settings can share it."}},
		{"expired login", envelope("unauthorized", "Sign in to continue.", `{}`), 401,
			[]string{"unauthorized: Sign in to continue.", "Run `latere login` and try again"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.inviteStatus, s.inviteBody = tc.status, tc.body
			out, _, err := runApp(t, s, "share", "hello", "a@example.com", "b@")
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

// ---- unshare ----

func TestAppUnshareTakesBackByAddress(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "unshare", "hello", "Pilot@Harbor.Example", "nobody@example.com", "ines@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if out != "No longer invited to hello:\n  Pilot@Harbor.Example\n  ines@example.com\n"+
		"Not invited to hello, so nothing to take back:\n  nobody@example.com\n\n"+
		"An invitation no one accepted is gone at once. A person who already opened the app is refused shortly after.\n" {
		t.Errorf("unshare =\n%s", out)
	}
	base := "/v1/account/apps/" + stubAppID + "/access"
	if got := s.seenAtThePlatform(); !slices.Equal(got, []string{"GET " + base, "DELETE " + base + "/inv_01JA4T0000000000000000000B", "DELETE " + base + "/inv_01JA4S0000000000000000000A"}) {
		t.Errorf("requests at the platform = %v", got)
	}
	eachAPIItsOwnAudience(t, s)
}

// An address that is not invited is no error: the end the person asked for
// already holds, as the platform answers a removal of nothing.
func TestAppUnshareOfAnAddressNotInvited(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	out, _, err := runAppIn(t, s, dir, "", "unshare", "nobody@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Not invited to hello, so nothing to take back:\n  nobody@example.com\n" {
		t.Errorf("unshare =\n%s", out)
	}
	if got := s.seenAtThePlatform(); len(got) != 1 || !strings.HasPrefix(got[0], "GET ") {
		t.Errorf("requests at the platform = %v, want the read alone", got)
	}
}

// A removal that fails stops the command with exit 1, after it has said what
// it took back before.
func TestAppUnshareStopsAtAFailedRemoval(t *testing.T) {
	s := newStubApps(t)
	s.revokeStatus = 503
	s.revokeBody = `{"error":{"code":"apps_unavailable","message":"Sharing could not be reached. Try again shortly.","details":{"detail":"revoke failed"}}}`
	s.revokeFailsOn = "inv_01JA4T0000000000000000000B"
	out, _, err := runApp(t, s, "unshare", "hello", "ines@example.com", "pilot@harbor.example", "ines@example.com")
	if err == nil {
		t.Fatal("want the failure")
	}
	wantContains(t, err.Error(), "take back the invitation of pilot@harbor.example: apps_unavailable: Sharing could not be reached. Try again shortly.\ndetail: revoke failed")
	wantContains(t, out, "No longer invited to hello:\n  ines@example.com\n")
	wantLacks(t, out, "pilot@harbor.example")
	if got := s.seenAtThePlatform(); len(got) != 3 {
		t.Errorf("requests at the platform = %v, want the read and two removals", got)
	}
	if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestAppUnshareRefusedTheList(t *testing.T) {
	s := newStubApps(t)
	s.accessStatus = 403
	s.accessBody = `{"error":{"code":"role_insufficient","message":"Only the people who can change this app's settings can share it.","details":{"reason":"not_owner"}}}`
	out, _, err := runApp(t, s, "unshare", "hello", "ines@example.com")
	if err == nil || !strings.Contains(err.Error(), "role_insufficient: Only the people who can change this app's settings can share it.") {
		t.Errorf("unshare = %v", err)
	}
	if out != "" || len(s.seenAtThePlatform()) != 1 {
		t.Errorf("stdout = %q, requests = %v", out, s.seenAtThePlatform())
	}
}
