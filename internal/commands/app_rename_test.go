// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The push URLs of the stub's app before and after a rename of hello to
// hello-world.
const (
	stubOldPush = "https://code.latere.ai/u-1/hello.git"
	stubNewPush = "https://code.latere.ai/u-1/hello-world.git"
)

// renameRequests is what a rename of hello sends: the read that names the
// old push URL, then the change.
var renameRequests = []string{"GET /v1/apps/apps/hello", "PATCH /v1/apps/apps/hello"}

func TestAppRenameMovesTheApp(t *testing.T) {
	s := newStubApps(t)
	out, errOut, err := runApp(t, s, "rename", "hello-world", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.seen(); !slices.Equal(got, renameRequests) {
		t.Errorf("requests = %v", got)
	}
	if len(s.patched) != 1 || s.patched["slug"] != "hello-world" {
		t.Errorf("change body = %v, want the slug alone", s.patched)
	}
	wantContains(t, out,
		"app:        hello-world\naddress:    https://hello-world.latere.site\npreview:    https://next--hello-world.latere.site\npush:       "+stubNewPush+"\n",
		"\nhttps://hello.latere.site stopped working, and the app's previews moved with it. Nothing sends a visitor on to the new address, so links to the old one stop working.\n",
		"The slug hello stays held for your account for seven days: nobody else can take it, and `latere apps rename hello hello-world` takes it back.\n",
		"\nThis directory is not a git repository, so no remote was changed. In a project that pushes to the app, run:\n  git remote set-url latere "+stubNewPush+"\n")
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing", errOut)
	}
	// A rename is the Apps API's alone: nothing of it is the platform's.
	if got := s.seenAtThePlatform(); len(got) != 0 {
		t.Errorf("requests at the platform = %v, want none", got)
	}
	if _, old := s.apps["hello"]; old {
		t.Error("the stub still has an app at the old slug")
	}
}

// The remote `apps create` added names the app's old push URL, so the
// command moves it, and reads the slug from it when none is given.
func TestAppRenameSetsTheRemote(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"the slug read from the remote", []string{"rename", "hello-world"}},
		{"the slug given", []string{"rename", "hello-world", "hello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			dir := gitRepo(t)
			gitRun(t, dir, "remote", "add", "latere", stubOldPush)
			gitRun(t, dir, "remote", "add", "origin", "https://example.com/hello.git")
			out, _, err := runAppIn(t, s, dir, "", tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if got := gitRun(t, dir, "remote", "get-url", "latere"); got != stubNewPush {
				t.Errorf("remote latere = %q, want the new push URL", got)
			}
			if got := gitRun(t, dir, "remote", "get-url", "origin"); got != "https://example.com/hello.git" {
				t.Errorf("remote origin = %q, want it unchanged", got)
			}
			wantContains(t, out, "push:       "+stubNewPush+"\n", "\nSet the git remote latere to the new push URL.\n")
			wantLacks(t, out, "git remote set-url")
			if got := s.seen(); !slices.Equal(got, renameRequests) {
				t.Errorf("requests = %v", got)
			}
		})
	}
}

// A remote that names anything but the app's old push URL is not this
// command's to change: it is left, with the line that sets it.
func TestAppRenameLeavesARemoteThatPointsElsewhere(t *testing.T) {
	const line = "git remote set-url latere " + stubNewPush
	for _, tc := range []struct {
		name, remote, url string
		args              []string
		want              string
	}{
		{"the same slug under another owner", "latere", "https://code.latere.ai/acme/hello.git", []string{"rename", "hello-world"},
			"\nThe git remote latere points at https://code.latere.ai/acme/hello.git, not at this app's old push URL; it was left as it is.\nTo point it at the app, run:\n  " + line + "\n"},
		{"another app", "latere", "https://code.latere.ai/u-1/other.git", []string{"rename", "hello-world", "hello"},
			"\nThe git remote latere points at https://code.latere.ai/u-1/other.git, not at this app's old push URL; it was left as it is.\nTo point it at the app, run:\n  " + line + "\n"},
		{"the old push URL written another way", "latere", "https://code.latere.ai/u-1/hello", []string{"rename", "hello-world", "hello"},
			"not at this app's old push URL; it was left as it is.\nTo point it at the app, run:\n  " + line + "\n"},
		{"no remote named latere", "origin", stubOldPush, []string{"rename", "hello-world", "hello"},
			"\nThis repository has no git remote named latere, so none was changed. In a project that pushes to the app, run:\n  " + line + "\n"},
		{"the new push URL already", "latere", stubNewPush, []string{"rename", "hello-world", "hello"},
			"\nThe git remote latere already points at the new push URL.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			dir := gitRepo(t)
			gitRun(t, dir, "remote", "add", tc.remote, tc.url)
			out, _, err := runAppIn(t, s, dir, "", tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			wantContains(t, out, "app:        hello-world\n", tc.want)
			wantLacks(t, out, "Set the git remote")
			if got := gitRun(t, dir, "remote", "get-url", tc.remote); got != tc.url {
				t.Errorf("remote %s = %q, want it unchanged", tc.remote, got)
			}
			if got := gitRun(t, dir, "remote"); got != tc.remote {
				t.Errorf("remotes = %q, want %s alone", got, tc.remote)
			}
			if s.patched["slug"] != "hello-world" {
				t.Errorf("change body = %v", s.patched)
			}
		})
	}
}

func TestBrokenSlugRule(t *testing.T) {
	for _, tc := range []struct{ slug, rule string }{
		{"hello", ""},
		{"a", ""},
		{"h3llo-w0rld", ""},
		{strings.Repeat("x", appSlugMax), ""},
		{strings.Repeat("x", appSlugMax+1), slugRuleLength},
		{"", slugRuleLength},
		{"Hello", slugRuleCharset},
		{"hello_world", slugRuleCharset},
		{"hello world", slugRuleCharset},
		{"héllo", slugRuleCharset},
		{"hello.site", slugRuleCharset},
		{"-hello", slugRuleBoundary},
		{"hello-", slugRuleBoundary},
		{"-", slugRuleBoundary},
		{"hello--world", slugRuleDoubleHyphen},
	} {
		if got := brokenSlugRule(tc.slug); got != tc.rule {
			t.Errorf("brokenSlugRule(%q) = %q, want %q", tc.slug, got, tc.rule)
		}
	}
	// Every rule the platform names has its sentence, the short one included.
	for _, rule := range []string{slugRuleLength, slugRuleCharset, slugRuleBoundary, slugRuleDoubleHyphen, slugRuleShort} {
		if invalidSlugReasons[rule] == "" {
			t.Errorf("the rule %s has no sentence", rule)
		}
	}
	if want := fmt.Sprintf("a slug is 1 to %d characters", appSlugMax); invalidSlugReasons[slugRuleLength] != want {
		t.Errorf("the length rule = %q, want %q", invalidSlugReasons[slugRuleLength], want)
	}
}

// A slug that breaks a rule is refused before the app is read, so nothing is
// sent and nothing of the repository changes.
func TestAppRenameSendsNothingForASlugThatBreaksARule(t *testing.T) {
	for _, tc := range []struct{ name, slug, want string }{
		{"empty", "", `"" cannot be an app's slug: a slug is 1 to 40 characters; nothing was sent`},
		{"too long", strings.Repeat("x", 41), `cannot be an app's slug: a slug is 1 to 40 characters; nothing was sent`},
		{"an upper case letter", "Hello", `"Hello" cannot be an app's slug: a slug takes only lowercase letters, digits and hyphens; nothing was sent`},
		{"an underscore", "hello_world", `"hello_world" cannot be an app's slug: a slug takes only lowercase letters, digits and hyphens; nothing was sent`},
		{"a hyphen first", "-hello", `"-hello" cannot be an app's slug: a slug starts and ends with a letter or a digit; nothing was sent`},
		{"a hyphen last", "hello-", `"hello-" cannot be an app's slug: a slug starts and ends with a letter or a digit; nothing was sent`},
		{"two hyphens in a row", "hello--world", `"hello--world" cannot be an app's slug: a slug has no two hyphens in a row; nothing was sent`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			dir := gitRepo(t)
			gitRun(t, dir, "remote", "add", "latere", stubOldPush)
			// "--" ends the flags, so a slug that starts with a hyphen is
			// read as the argument it is.
			out, errOut, err := runAppIn(t, s, dir, "", "rename", "--", tc.slug)
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want)
			if len(s.seen()) != 0 || len(s.audiences) != 0 {
				t.Errorf("requests = %v, mints = %v, want neither", s.seen(), s.audiences)
			}
			if out != "" || errOut != "" {
				t.Errorf("stdout = %q, stderr = %q, want neither", out, errOut)
			}
			if got := gitRun(t, dir, "remote", "get-url", "latere"); got != stubOldPush {
				t.Errorf("remote latere = %q, want it unchanged", got)
			}
			var buf bytes.Buffer
			if code := HandleExitError(&buf, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

// A slug under three characters is a plan's to allow, so it is sent, and the
// platform's refusal is the answer.
func TestAppRenameLeavesAShortSlugToThePlatform(t *testing.T) {
	s := newStubApps(t)
	s.patchStatus, s.patchBody = 400, `{"error":{"code":"invalid_slug","message":"This slug is not a valid address.","details":{"reason":"short","hint":"Use 1 to 40 lowercase letters, digits, and single hyphens."}}}`
	_, _, err := runApp(t, s, "rename", "hi", "hello")
	if err == nil {
		t.Fatal("want a refusal")
	}
	wantContains(t, err.Error(), "invalid_slug: The slug hi is not a valid address: a slug under three characters needs a plan that allows short slugs.")
	if got := s.seen(); !slices.Equal(got, renameRequests) {
		t.Errorf("requests = %v", got)
	}
}

// The platform makes no change of a slug that is the app's own, so the
// command asks for none and says so.
func TestAppRenameToTheSlugTheAppHas(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", stubOldPush)
	out, _, err := runAppIn(t, s, dir, "", "rename", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.seen(); !slices.Equal(got, []string{"GET /v1/apps/apps/hello"}) {
		t.Errorf("requests = %v, want the read alone", got)
	}
	wantContains(t, out, "app:        hello\naddress:    https://hello.latere.site\n", "push:       "+stubOldPush+"\n", "\nhello has this slug already, so nothing changed.\n")
	wantLacks(t, out, "stopped working", "held for")
	if got := gitRun(t, dir, "remote", "get-url", "latere"); got != stubOldPush {
		t.Errorf("remote latere = %q, want it unchanged", got)
	}
}

func TestAppRenameOfAnOrganizationsApp(t *testing.T) {
	s := newStubApps(t)
	s.owner = "organization"
	s.apps["hello"] = stubAppAs("hello", "private", "organization")
	out, _, err := runApp(t, s, "rename", "hello-world", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "The slug hello stays held for the organization that owns the app for seven days: nobody else can take it")
	wantLacks(t, out, "your account")
}

func TestAppRenameJSONIsTheAPIsApp(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", stubOldPush)
	out, errOut, err := runAppIn(t, s, dir, "", "rename", "hello-world", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	// The API's own bytes: a field the command reads nowhere is still there.
	if got["slug"] != "hello-world" || got["url"] != "https://hello-world.latere.site" || got["cluster"] == nil {
		t.Errorf("json = %v", got)
	}
	if errOut != "\nSet the git remote latere to the new push URL.\n" {
		t.Errorf("stderr = %q, want what became of the remote", errOut)
	}
	if got := gitRun(t, dir, "remote", "get-url", "latere"); got != stubNewPush {
		t.Errorf("remote latere = %q, want the new push URL", got)
	}

	// A slug the app has prints the app as it is, and says so on stderr.
	out, errOut, err = runAppIn(t, s, dir, "", "rename", "hello-world", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["slug"] != "hello-world" {
		t.Errorf("json = %v (%v): %q", got, err, out)
	}
	if errOut != "\nhello-world has this slug already, so nothing changed.\n" {
		t.Errorf("stderr = %q", errOut)
	}
}

// Every refusal has its own sentence under its code, exits 1, prints nothing
// on standard output, and leaves the remote as it was.
func TestAppRenameRefusals(t *testing.T) {
	envelope := func(code, message, details string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"details":%s}}`, code, message, details)
	}
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
	}{
		{"taken", envelope("slug_taken", "This slug is already in use.", `{"slug":"hello-world","hint":"Pick another slug or leave it out to get a generated one."}`), 409,
			[]string{"slug_taken: The slug hello-world is taken: another app has it, or it is held for the account that gave it up.\nPick another slug"}},
		{"reserved", envelope("slug_reserved", "This slug is reserved by the platform.", `{"slug":"hello-world","hint":"Pick another slug."}`), 409,
			[]string{"slug_reserved: The slug hello-world is reserved by the platform.\nPick another slug."}},
		{"held until a moment", envelope("slug_held", "This slug was released recently and is held for its previous owner.", `{"slug":"hello-world","released_at":"2026-10-05T09:00:00Z","hold_ends_at":"2026-10-12T09:00:00Z","hint":"Pick another slug or wait seven days."}`), 409,
			[]string{"slug_held: The slug hello-world was released recently and is held for its previous owner until 2026-10-12 09:00 UTC.\nPick another slug or wait seven days."}},
		{"held without its end", envelope("slug_held", "This slug was released recently and is held for its previous owner.", `{"slug":"hello-world"}`), 409,
			[]string{"slug_held: The slug hello-world was released recently and is held for its previous owner."}},
		{"a deleted repository's name", envelope("slug_held", "This slug was released recently and is held for its previous owner.", `{"reason":"repository_name_held","hint":"Pick another slug or wait seven days."}`), 409,
			[]string{"slug_held: The slug hello-world is the name of a repository that was deleted recently, and the name is still held.\nPick another slug or wait seven days."}},
		{"a rule the platform found broken", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"boundary","hint":"h"}`), 400,
			[]string{"invalid_slug: The slug hello-world is not a valid address: a slug starts and ends with a letter or a digit."}},
		{"a rule this version does not know", envelope("invalid_slug", "This slug is not a valid address.", `{"reason":"new_rule","hint":"h"}`), 400,
			[]string{"invalid_slug: The slug hello-world is not a valid address.\nh"}},
		{"a repository of that name", envelope("repository_name_taken", "The account this app belongs to already has a repository with this name.", `{"stage":"rename","hint":"Pick another slug, or rename or delete that repository first."}`), 409,
			[]string{"repository_name_taken: The owner of hello already has a repository named hello-world, and an app's repository takes the app's slug as its name.\nPick another slug, or rename or delete that repository first."}},
		{"the repository did not answer", envelope("repository_unavailable", "The app's repository did not answer.", `{"operation":"rename","hint":"Deploy again in a few minutes. The platform has been notified."}`), 503,
			[]string{"repository_unavailable: The repository of hello could not be renamed, so nothing was changed. Run the command again in a few minutes."}},
		{"forbidden", envelope("forbidden", "You do not have permission to do this.", `{"action":"app.settings","reason":"role_insufficient","hint":"Ask an owner."}`), 403,
			[]string{"forbidden: You may not rename hello (role_insufficient).\nAsk an owner.\nThe app's owner can, and so can an owner or admin of the organization that owns it."}},
		{"forbidden without a reason", envelope("forbidden", "You do not have permission to do this.", `{"action":"app.settings"}`), 403,
			[]string{"forbidden: You may not rename hello.\nThe app's owner can"}},
		{"being deleted", envelope("app_deleting", "This app is being deleted.", `{"hint":"Create a new app."}`), 409,
			[]string{"app_deleting: This app is being deleted.\nCreate a new app."}},
		{"gone between the read and the change", envelope("app_not_found", "There is no app with this slug in this account.", `{"slug":"hello","hint":"Check the slug with latere apps list."}`), 404,
			[]string{"app_not_found: There is no app with this slug in this account.\nCheck the slug with latere apps list.\nThe slug hello was read from the git remote latere."}},
		{"refused body", envelope("invalid_request", "The request could not be read.", `{"field":"slug","reason":"type"}`), 400,
			[]string{"invalid_request: The platform refused the rename's slug."}},
		{"expired login", envelope("unauthenticated", "Sign in to continue.", `{"reason":"expired","hint":"Sign in again."}`), 401,
			[]string{"unauthenticated: Sign in to continue.", "Run `latere login` and try again."}},
		{"empty", "", 500, []string{"the Apps API answered HTTP 500"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.patchStatus, s.patchBody = tc.status, tc.body
			dir := gitRepo(t)
			gitRun(t, dir, "remote", "add", "latere", stubOldPush)
			out, errOut, err := runAppIn(t, s, dir, "", "rename", "hello-world")
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			if out != "" || errOut != "" {
				t.Errorf("stdout = %q, stderr = %q, want neither", out, errOut)
			}
			if got := s.seen(); !slices.Equal(got, renameRequests) {
				t.Errorf("requests = %v", got)
			}
			if got := gitRun(t, dir, "remote", "get-url", "latere"); got != stubOldPush {
				t.Errorf("remote latere = %q, want it unchanged", got)
			}
			var buf bytes.Buffer
			if code := HandleExitError(&buf, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

// An app the account does not have is said before any change is asked for,
// with where a slug that was not given came from.
func TestAppRenameOfAnUnknownApp(t *testing.T) {
	s := newStubApps(t)
	_, _, err := runApp(t, s, "rename", "hello-world", "gone")
	if err == nil {
		t.Fatal("want a refusal")
	}
	wantContains(t, err.Error(), "app_not_found: There is no app with this slug in this account.")
	wantLacks(t, err.Error(), "was read from the git remote")
	if got := s.seen(); !slices.Equal(got, []string{"GET /v1/apps/apps/gone"}) {
		t.Errorf("requests = %v, want the read alone", got)
	}

	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/gone.git")
	_, _, err = runAppIn(t, s, dir, "", "rename", "hello-world")
	if err == nil {
		t.Fatal("want a refusal")
	}
	wantContains(t, err.Error(), "app_not_found", "The slug gone was read from the git remote latere.")
	if got := gitRun(t, dir, "remote", "get-url", "latere"); got != "https://code.latere.ai/u-1/gone.git" {
		t.Errorf("remote latere = %q, want it unchanged", got)
	}
}

// The rename is made before the remote is set, so a remote git cannot write
// fails the command with the app renamed, and says the line that sets it.
func TestAppRenameSaysARemoteItCouldNotSet(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only directory")
	}
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", stubOldPush)
	gitDir := filepath.Join(dir, ".git")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(gitDir, 0o755); err != nil {
			t.Errorf("restore %s: %v", gitDir, err)
		}
	})
	out, _, err := runAppIn(t, s, dir, "", "rename", "hello-world")
	if err == nil {
		t.Fatal("want a failure")
	}
	wantContains(t, err.Error(), "the app was renamed to hello-world, but setting the git remote latere failed", "Set it with: git remote set-url latere "+stubNewPush)
	wantContains(t, out, "app:        hello-world\n")
	if got := gitRun(t, dir, "remote", "get-url", "latere"); got != stubOldPush {
		t.Errorf("remote latere = %q, want the one git could not change", got)
	}
}

func TestAppRenameArguments(t *testing.T) {
	for _, args := range [][]string{{"rename"}, {"rename", "a", "b", "c"}} {
		s := newStubApps(t)
		_, _, err := runApp(t, s, args...)
		if err == nil || !strings.Contains(err.Error(), "accepts between 1 and 2 arg(s)") {
			t.Errorf("%v = %v", args, err)
		}
		if len(s.seen()) != 0 {
			t.Errorf("requests = %v, want none", s.seen())
		}
	}
}

// The help says what a rename does to the old address and to the remote,
// with the bounds of a slug and of the hold from the one place each is kept.
func TestAppRenameHelp(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "rename", "--help")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "latere apps rename <new-slug> [slug]",
		"The old address stops working at once, and nothing sends a visitor on from\nit to the new one",
		"stays held for\n"+appSlugHold+": nobody else can take it",
		"the command sets that remote to the new one",
		"A slug is "+appSlugGrammar+",\nstarting and ending with a letter or a digit.",
		"refused before anything is sent",
		"Without [slug], the slug is read from the git remote latere.")
	if len(s.seen()) != 0 {
		t.Errorf("requests = %v, want none", s.seen())
	}
	// The group's help names the command.
	group, _, err := runApp(t, s)
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, group, "rename  ", "latere apps rename hello-world", "'latere apps\nrename <new-slug>' changes it. The old address stops working at once.")
}
