// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A name change sends the name alone, trimmed, so the slug and the address
// stay, and prints the app with them.
func TestAppNameChangesTheNameAlone(t *testing.T) {
	s := newStubApps(t)
	out, errOut, err := runApp(t, s, "name", "  Latere design boards ", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.seen(); !slices.Equal(got, []string{"PATCH /v1/apps/apps/hello"}) {
		t.Errorf("requests = %v", got)
	}
	if len(s.patched) != 1 || s.patched["name"] != "Latere design boards" {
		t.Errorf("change body = %v, want the name alone", s.patched)
	}
	wantContains(t, out, "app:        hello\n", "address:    https://hello.latere.site\n", "address and push URL stay as they were")
	if errOut != "" {
		t.Errorf("stderr = %q", errOut)
	}
	if got := s.seenAtThePlatform(); len(got) != 0 {
		t.Errorf("requests at the platform = %v, want none", got)
	}
}

// The slug is read from the git remote latere when it is left out, and
// --json prints the app as the API answers it.
func TestAppNameReadsTheSlugFromTheRemote(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", stubOldPush)
	out, _, err := runAppIn(t, s, dir, "", "name", "Hello world", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.seen(); !slices.Equal(got, []string{"PATCH /v1/apps/apps/hello"}) {
		t.Errorf("requests = %v", got)
	}
	var app map[string]any
	if err := json.Unmarshal([]byte(out), &app); err != nil || app["slug"] != "hello" {
		t.Errorf("--json = %v\n%s", err, out)
	}
}

// A blank name is refused before anything is sent, and a refusal says what
// it was about.
func TestAppNameRefusals(t *testing.T) {
	s := newStubApps(t)
	if _, _, err := runApp(t, s, "name", "  ", "hello"); err == nil || !strings.Contains(err.Error(), "cannot be blank; nothing was sent") {
		t.Errorf("a blank name = %v", err)
	}
	if got := s.seen(); len(got) != 0 {
		t.Errorf("a blank name sent %v", got)
	}
	for _, tc := range []struct {
		status    int
		body, err string
	}{
		{403, `{"error":{"code":"forbidden","message":"You do not have permission to do this.","details":{"action":"app.settings","reason":"not_owner"}}}`,
			"forbidden: You may not change the name of hello (not_owner).\nThe app's owner can"},
		{400, `{"error":{"code":"invalid_request","message":"The request is not valid.","details":{"field":"name"}}}`,
			"invalid_request: The platform refused the name of hello, which may not be blank."},
		{409, `{"error":{"code":"app_deleting","message":"The app is being deleted.","details":{}}}`,
			"app_deleting: hello is being deleted, so its name was not changed."},
	} {
		s.patchStatus, s.patchBody = tc.status, tc.body
		_, _, err := runApp(t, s, "name", "Hello", "hello")
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("a %d = %v, want %q", tc.status, err, tc.err)
		}
	}
}

// --name on rename, which changes the address, sends nothing and names the
// command that changes the name, with the app's slug when one was given.
func TestAppRenameWithANamePointsAtName(t *testing.T) {
	s := newStubApps(t)
	for args, want := range map[string]string{
		"rename --name Boards":       "latere apps name \"Boards\"\n",
		"rename --name Boards hello": "latere apps name \"Boards\" hello",
	} {
		_, _, err := runApp(t, s, strings.Fields(args)...)
		if err == nil || !strings.Contains(err.Error(), "changes an app's address, and nothing was sent") || !strings.Contains(err.Error()+"\n", want) {
			t.Errorf("%s = %v, want %q", args, err, want)
		}
	}
	if got := s.seen(); len(got) != 0 {
		t.Errorf("rename --name sent %v", got)
	}
}
