// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// generatedShape is a generated password as every Latere surface makes one:
// 4 groups of 5 characters from the alphabet without look-alikes, joined by
// hyphens. The alphabet is written out here, so a change of the command's
// own fails the tests.
var generatedShape = regexp.MustCompile(`^[abcdefghjkmnpqrstuvwxyz23456789]{5}(-[abcdefghjkmnpqrstuvwxyz23456789]{5}){3}$`)

// The sentences a private app with a password just set is described by.
const (
	stubPasswordChoice = "Anyone with the password: whoever types it at the app's address opens the app, with no Latere account.\n" +
		"You and the people you invite still open the app by signing in with a Latere account.\n"
	stubInvitedChoice = "Only people invited: it opens for them and for you, each after signing in with a Latere account.\n"
	stubSendIt        = "Send people the app's address and the password. Neither is emailed.\n"
)

// The two writes of a password at the platform, and the change of an app at
// the Apps API, as the stub's ordered log names them.
const (
	stubPasswordPath = stubAccessPath + "/password"
	stubReadApp      = "apps GET /v1/apps/apps/hello"
	stubChangeApp    = "apps PATCH /v1/apps/apps/hello"
	stubReadAccess   = "platform GET " + stubAccessPath
	stubSetPassword  = "platform PUT " + stubPasswordPath
	stubDropPassword = "platform DELETE " + stubPasswordPath
)

// privateStub is the stub with hello private, as an app is before a
// password is set on it.
func privateStub(t *testing.T) *stubApps {
	t.Helper()
	s := newStubApps(t)
	s.apps["hello"] = stubAppAs("hello", "private", "personal")
	return s
}

// typedAtATerminal stands in for a terminal at which each of answers is
// typed in turn, without echo.
func typedAtATerminal(t *testing.T, answers ...string) {
	t.Helper()
	terminal := typedSecret
	t.Cleanup(func() { typedSecret = terminal })
	typedSecret = func(io.Reader) func() (string, error) {
		return func() (string, error) {
			if len(answers) == 0 {
				return "", io.EOF
			}
			answer := answers[0]
			answers = answers[1:]
			return answer, nil
		}
	}
}

// nowhere fails when secret is in the error or in any of texts: a password
// that was typed or piped is printed by nothing.
func nowhere(t *testing.T, secret string, err error, texts ...string) {
	t.Helper()
	if err != nil {
		texts = append(texts, err.Error())
	}
	for _, text := range texts {
		if strings.Contains(text, secret) {
			t.Errorf("the password is in:\n%s", text)
		}
	}
}

// ---- the generator ----

func TestGeneratedPasswordShape(t *testing.T) {
	seen, drawn := map[string]bool{}, map[rune]bool{}
	for range 200 {
		p, err := generatePassword()
		if err != nil {
			t.Fatal(err)
		}
		if !generatedShape.MatchString(p) {
			t.Fatalf("generated %q, want 4 groups of 5 from the alphabet without look-alikes", p)
		}
		if seen[p] {
			t.Fatalf("generated %q twice", p)
		}
		seen[p] = true
		for _, r := range p {
			drawn[r] = true
		}
	}
	// 4000 draws leave out one of 31 characters with a chance under 1e-55,
	// so a character never drawn is one the generator cannot reach.
	for _, r := range "abcdefghjkmnpqrstuvwxyz23456789" {
		if !drawn[r] {
			t.Errorf("%q was never drawn", r)
		}
	}
}

func TestGeneratedPasswordsDiffer(t *testing.T) {
	a, errA := generatePassword()
	b, errB := generatePassword()
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a == b {
		t.Errorf("two calls generated the same password")
	}
}

// ---- set ----

func TestAppPasswordGeneratesOneAndPrintsItOnce(t *testing.T) {
	s := privateStub(t)
	out, errOut, err := runApp(t, s, "password", "hello", "--generate")
	if err != nil {
		t.Fatal(err)
	}
	sent := s.passwordSent(t)
	if !generatedShape.MatchString(sent) {
		t.Fatalf("the platform was sent %q, want a generated password", sent)
	}
	if out != "app:        hello\naddress:    https://hello.latere.site\nvisibility: private\n\n"+
		"The password of hello is set.\n"+stubPasswordChoice+stubSendIt+
		"\nThe generated password, printed this once:\n"+sent+"\n" {
		t.Errorf("password --generate =\n%s", out)
	}
	if n := strings.Count(out+errOut, sent); n != 1 {
		t.Errorf("the generated password is printed %d times, want once", n)
	}
	// The app was private already, so nothing of it changed.
	if got := s.inOrder(); !slices.Equal(got, []string{stubReadApp, stubReadAccess, stubSetPassword}) {
		t.Errorf("requests in order = %v", got)
	}
	eachAPIItsOwnAudience(t, s)
}

// A password is for a private app, so an app anyone with the link opens is
// made private before the password is sent, and the command says so.
func TestAppPasswordMakesTheAppPrivateFirst(t *testing.T) {
	for _, tc := range []struct {
		before string
		want   []string
	}{
		{"unlisted", []string{"The app was unlisted before and is private now, so its address alone no longer opens it.\n" +
			"If you had asked to show it in the gallery, that request has ended.\n" + stubSendIt}},
		{"public", []string{"The app was public before and is private now, so its address alone no longer opens it.\n" +
			"If it was shown in the gallery, it has left the gallery.\n" + stubSendIt}},
	} {
		t.Run(tc.before, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", tc.before, "personal")
			out, _, err := runApp(t, s, "password", "hello", "--generate")
			if err != nil {
				t.Fatal(err)
			}
			if got := s.inOrder(); !slices.Equal(got, []string{stubReadApp, stubReadAccess, stubChangeApp, stubSetPassword}) {
				t.Errorf("requests in order = %v", got)
			}
			if len(s.patched) != 1 || s.patched["visibility"] != "private" {
				t.Errorf("change body = %v, want the visibility private alone", s.patched)
			}
			wantContains(t, out, append(tc.want, "visibility: private\n\nThe password of hello is set.\n"+stubPasswordChoice)...)
			if !strings.HasSuffix(out, "\n"+s.passwordSent(t)+"\n") {
				t.Errorf("the generated password is not the last line:\n%s", out)
			}
		})
	}
}

func TestAppPasswordOfAnAppThatHasOneReplacesIt(t *testing.T) {
	s := privateStub(t)
	s.access = stubAccessWithPassword()
	out, _, err := runApp(t, s, "password", "hello", "--generate")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "\nThe password of hello is replaced. The one before stops opening the app within about a minute.\n"+stubPasswordChoice)
	wantLacks(t, out, "is set.", "2026-10-08")
	if got := s.seenAtThePlatform(); !slices.Equal(got, []string{"GET " + stubAccessPath, "PUT " + stubPasswordPath}) {
		t.Errorf("requests at the platform = %v", got)
	}
}

// --stdin takes one line as it is: the line ending goes, LF or CRLF, and
// nothing else, so a space at either end is part of the password.
func TestAppPasswordFromStandardInput(t *testing.T) {
	for _, tc := range []struct {
		name, stdin, want string
	}{
		{"a line", "harbor wall at low tide\n", "harbor wall at low tide"},
		{"spaces at both ends are kept", "  harbor wall \t\n", "  harbor wall \t"},
		{"a CRLF line ending", "harbor wall at low tide\r\n", "harbor wall at low tide"},
		{"no line ending", "harbor wall at low tide", "harbor wall at low tide"},
		{"the first line alone", "harbor wall at low tide\nand a second line\n", "harbor wall at low tide"},
		{"any Unicode", "港の壁、干潮のとき\n", "港の壁、干潮のとき"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", "unlisted", "personal")
			out, errOut, err := runAppIn(t, s, t.TempDir(), tc.stdin, "password", "hello", "--stdin")
			if err != nil {
				t.Fatal(err)
			}
			if got := s.passwordSent(t); got != tc.want {
				t.Errorf("the platform was sent %q, want %q", got, tc.want)
			}
			nowhere(t, strings.TrimSpace(tc.want), nil, out, errOut)
			// The password goes to the platform alone: the change of the
			// app carries the visibility and nothing else.
			if len(s.patched) != 1 || s.patched["visibility"] != "private" {
				t.Errorf("change body = %v, want the visibility private alone", s.patched)
			}
			wantContains(t, out, "The password of hello is set.\n"+stubPasswordChoice)
			wantLacks(t, out, "generated")
			if !strings.HasSuffix(out, stubSendIt) || errOut != "" {
				t.Errorf("password --stdin =\n%s\nstderr = %q", out, errOut)
			}
		})
	}
}

// On a terminal the password is asked for twice, on standard error, and
// nothing typed is printed.
func TestAppPasswordTypedAtATerminal(t *testing.T) {
	const typed = "harbor wall at low tide"
	s := privateStub(t)
	typedAtATerminal(t, typed, typed)
	out, errOut, err := runApp(t, s, "password", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.passwordSent(t); got != typed {
		t.Errorf("the platform was sent %q, want what was typed", got)
	}
	if errOut != "Password for hello: \nType it again: \n" {
		t.Errorf("prompts = %q", errOut)
	}
	nowhere(t, typed, nil, out, errOut)
	nowhere(t, "harbor", nil, out, errOut)
	wantContains(t, out, "The password of hello is set.\n"+stubPasswordChoice+stubSendIt)
	wantLacks(t, out, "generated")
}

// Two answers that differ set nothing: a slip in a password no one saw
// would be one no one knows.
func TestAppPasswordTypedTwoWays(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = stubAppAs("hello", "unlisted", "personal")
	typedAtATerminal(t, "harbor wall at low tide", "harbor wall at low tied")
	out, errOut, err := runApp(t, s, "password", "hello")
	if err == nil || !strings.Contains(err.Error(), "the two passwords typed are not the same. Nothing was changed") {
		t.Fatalf("password = %v", err)
	}
	nowhere(t, "harbor", err, out, errOut)
	if out != "" || len(s.inOrder()) != 0 {
		t.Errorf("stdout = %q, requests = %v, want neither", out, s.inOrder())
	}
	if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

// The password is asked for before anything is asked of either API: the
// terminal shows what is typed until the prompt reads, so the prompt does
// not wait for the platform.
func TestAppPasswordIsAskedForBeforeAnyRequest(t *testing.T) {
	s := privateStub(t)
	terminal := typedSecret
	t.Cleanup(func() { typedSecret = terminal })
	var madeAtThePrompts [][]string
	typedSecret = func(io.Reader) func() (string, error) {
		return func() (string, error) {
			madeAtThePrompts = append(madeAtThePrompts, s.inOrder())
			return "harbor wall at low tide", nil
		}
	}
	if _, _, err := runApp(t, s, "password", "hello"); err != nil {
		t.Fatal(err)
	}
	if len(madeAtThePrompts) != 2 || len(madeAtThePrompts[0]) != 0 || len(madeAtThePrompts[1]) != 0 {
		t.Errorf("requests made at each prompt = %v, want two prompts and none", madeAtThePrompts)
	}
	if got := s.inOrder(); !slices.Equal(got, []string{stubReadApp, stubReadAccess, stubSetPassword}) {
		t.Errorf("requests in order = %v", got)
	}
}

// A terminal that ends before the password does sets nothing.
func TestAppPasswordPromptThatEnds(t *testing.T) {
	s := privateStub(t)
	typedAtATerminal(t, "harbor wall at low tide")
	_, _, err := runApp(t, s, "password", "hello")
	if err == nil || !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "read the password: ") {
		t.Fatalf("password = %v", err)
	}
	nowhere(t, "harbor", err)
	if len(s.passwords) != 0 {
		t.Errorf("sets of the password = %d, want none", len(s.passwords))
	}
}

// Without a terminal there is no prompt, and the refusal names the two
// ways to give a password, before any request.
func TestAppPasswordWithoutATerminal(t *testing.T) {
	s := privateStub(t)
	out, _, err := runAppIn(t, s, t.TempDir(), "harbor wall at low tide\n", "password", "hello")
	if err == nil {
		t.Fatal("want a refusal")
	}
	wantContains(t, err.Error(), "there is no terminal to type the password at", "--stdin", "--generate")
	nowhere(t, "harbor", err, out)
	if len(s.inOrder()) != 0 || out != "" {
		t.Errorf("requests = %v, stdout = %q, want neither", s.inOrder(), out)
	}
}

// A password is never an argument or a flag's value, and the refusal
// repeats nothing it was given.
func TestAppPasswordIsNeverAnArgument(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"password", "hello", "harbor-wall-at-low-tide"}, "latere apps password takes the slug alone: a password is never an argument"},
		{[]string{"password", "hello", "harbor-wall-at-low-tide", "--generate"}, "a password is never an argument"},
		{[]string{"password", "hello", "--password", "harbor-wall-at-low-tide"}, "a flag given to latere apps password could not be read"},
		{[]string{"password", "hello", "--password=harbor-wall-at-low-tide"}, "A password is never a flag's value: `latere apps password --help` lists the flags"},
		{[]string{"password", "hello", "--stdin=harbor-wall-at-low-tide"}, "is not repeated here in case it holds a password"},
		{[]string{"password", "hello", "-pharbor-wall-at-low-tide"}, "a flag given to latere apps password could not be read"},
	} {
		t.Run(strings.Join(tc.args[2:], " "), func(t *testing.T) {
			s := privateStub(t)
			out, errOut, err := runApp(t, s, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("password = %v, want %q", err, tc.want)
			}
			nowhere(t, "harbor", err, out, errOut)
			if len(s.inOrder()) != 0 || out != "" {
				t.Errorf("requests = %v, stdout = %q, want neither", s.inOrder(), out)
			}
		})
	}
}

// The bounds of a password are the ones the access answer names, counted in
// Unicode code points, and a password outside them is refused before
// anything is sent or changed.
func TestAppPasswordIsHeldToTheBoundsThePlatformNames(t *testing.T) {
	bounded := func(fewest, most string) string {
		return strings.Replace(stubAccessJSON(), `"min_password":8,"max_password":128`, `"min_password":`+fewest+`,"max_password":`+most, 1)
	}
	for _, tc := range []struct {
		name, access, stdin, want string
	}{
		{"shorter than the fewest", stubAccessJSON(), "seven77\n", "a password is at least 8 characters, and this one is shorter. Nothing was changed"},
		{"the fewest", stubAccessJSON(), "eight888\n", ""},
		{"the most, in characters of two bytes", stubAccessJSON(), strings.Repeat("é", 128) + "\n", ""},
		{"longer than the most", stubAccessJSON(), strings.Repeat("é", 129) + "\n", "a password is at most 128 characters, and this one is longer. Nothing was changed"},
		{"another platform's fewest", bounded("12", "16"), "eleven11111\n", "a password is at least 12 characters, and this one is shorter"},
		{"another platform's most", bounded("12", "16"), "seventeen17171717\n", "a password is at most 16 characters, and this one is longer"},
		{"an empty line", stubAccessJSON(), "\n", "--stdin read no password: the first line of standard input is empty. Nothing was changed"},
		{"nothing at all", stubAccessJSON(), "", "--stdin read no password"},
		{"a line past what is read", stubAccessJSON(), strings.Repeat("x", maxPasswordLine+10) + "\n", "a password is at most 128 characters, and this one is longer"},
		{"text that is not UTF-8", stubAccessJSON(), "harbor wall \xff\xfe at low tide\n", "the password is not UTF-8 text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", "unlisted", "personal")
			s.access = tc.access
			out, errOut, err := runAppIn(t, s, t.TempDir(), tc.stdin, "password", "hello", "--stdin")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if got := s.passwordSent(t); got != strings.TrimSuffix(tc.stdin, "\n") {
					t.Errorf("the platform was sent %d bytes, want the line", len(got))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("password = %v, want %q", err, tc.want)
			}
			if typed := strings.TrimSpace(tc.stdin); len(typed) > 4 {
				nowhere(t, typed[:5], err, out, errOut)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if got := s.inOrder(); !slices.Equal(got, []string{stubReadApp, stubReadAccess}) {
				t.Errorf("requests in order = %v, want the two reads alone", got)
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

// A platform that names no bounds holds a password to its own, and its
// refusal is said with the bounds it carries, never with the password.
func TestAppPasswordRefusedByThePlatformForItsLength(t *testing.T) {
	const piped = "short77"
	s := privateStub(t)
	s.access = strings.Replace(stubAccessJSON(), `,"min_password":8,"max_password":128`, "", 1)
	s.passwordStatus = 400
	s.passwordBody = `{"error":{"code":"invalid_field","message":"The request could not be read.","details":{"field":"password","reason":"length","min":8,"max":128,"detail":"password is 8 to 128 characters, each Unicode code point counted and nothing trimmed"}}}`
	out, errOut, err := runAppIn(t, s, t.TempDir(), piped+"\n", "password", "hello", "--stdin")
	if err == nil {
		t.Fatal("want a refusal")
	}
	wantContains(t, err.Error(), "invalid_field: The platform refused the password: a password is 8 to 128 characters. The app's password was not changed.\n"+
		"detail: password is 8 to 128 characters, each Unicode code point counted and nothing trimmed")
	wantLacks(t, err.Error(), "was made private")
	nowhere(t, piped, err, out, errOut)
	if got := s.passwordSent(t); got != piped {
		t.Errorf("the platform was sent %q, want the line: it names no bounds to hold it to", got)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

// A platform that sets no passwords is known before anything changes: the
// app is not made private for a password that cannot be set.
func TestAppPasswordWhereThePlatformSetsNone(t *testing.T) {
	for _, args := range [][]string{{"--generate"}, {"--stdin"}, {"--remove"}, {"--generate", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", "unlisted", "personal")
			s.access = stubAccessWithoutPasswords()
			out, errOut, err := runAppIn(t, s, t.TempDir(), "harbor wall at low tide\n", append([]string{"password", "hello"}, args...)...)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if err.Error() != "passwords_unconfigured: Passwords are not available on this platform yet. Nothing was changed." {
				t.Errorf("password = %v", err)
			}
			if got := s.inOrder(); !slices.Equal(got, []string{stubReadApp, stubReadAccess}) {
				t.Errorf("requests in order = %v, want the two reads alone", got)
			}
			if s.patched != nil || out != "" || errOut != "" {
				t.Errorf("change body = %v, stdout = %q, stderr = %q, want none", s.patched, out, errOut)
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
	t.Run("at a terminal", func(t *testing.T) {
		s := newStubApps(t)
		s.access = stubAccessWithoutPasswords()
		typedAtATerminal(t, "harbor wall at low tide", "harbor wall at low tide")
		out, errOut, err := runApp(t, s, "password", "hello")
		if err == nil || !strings.Contains(err.Error(), "Passwords are not available on this platform yet. Nothing was changed.") {
			t.Errorf("password = %v", err)
		}
		nowhere(t, "harbor", err, out, errOut)
		if s.patched != nil || len(s.passwords) != 0 {
			t.Errorf("change body = %v, sets of the password = %d, want neither", s.patched, len(s.passwords))
		}
	})
}

// A platform that stops setting passwords between the read and the set
// answers the set itself. An app that was private already is as it was; one
// that was made private for the password stays private, and the failure
// says so.
func TestAppPasswordRefusedWhereThePlatformSetsNone(t *testing.T) {
	const unconfigured = `{"error":{"code":"passwords_unconfigured","message":"Passwords for apps are not available yet.","details":{"detail":"PLATFORM_APP_PASSWORDS is not on"}}}`
	for _, tc := range []struct {
		before      string
		want, lacks []string
		order       []string
	}{
		{"private", []string{"passwords_unconfigured: Passwords are not available on this platform yet.\ndetail: PLATFORM_APP_PASSWORDS is not on"},
			[]string{"was made private"}, []string{stubReadApp, stubReadAccess, stubSetPassword}},
		{"unlisted", []string{"passwords_unconfigured: Passwords are not available on this platform yet.\ndetail: PLATFORM_APP_PASSWORDS is not on\n" +
			"hello was made private before the password was sent, and stays private. `latere apps access hello` says whether a password is set, and `latere apps visibility unlisted hello` opens the app to anyone with the link again"},
			nil, []string{stubReadApp, stubReadAccess, stubChangeApp, stubSetPassword}},
	} {
		t.Run(tc.before, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", tc.before, "personal")
			s.passwordStatus, s.passwordBody = 404, unconfigured
			out, _, err := runApp(t, s, "password", "hello", "--generate")
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			wantLacks(t, err.Error(), tc.lacks...)
			nowhere(t, s.passwordSent(t), err, out)
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if got := s.inOrder(); !slices.Equal(got, tc.order) {
				t.Errorf("requests in order = %v", got)
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

func TestAppPasswordRefusals(t *testing.T) {
	const piped = "harbor wall at low tide"
	for _, tc := range []struct {
		name, body  string
		status      int
		atTheRead   bool
		want, lacks []string
	}{
		{"not one who may share, at the read", `{"error":{"code":"role_insufficient","message":"Only the people who can change this app's settings can share it.","details":{"reason":"not_owner"}}}`, 403, true,
			[]string{"role_insufficient: Only the people who can change this app's settings can share it."}, nil},
		{"no such app at the platform, at the read", `{"error":{"code":"access_not_found","message":"We couldn't find that app.","details":{"app_id":"` + stubAppID + `"}}}`, 404, true,
			[]string{"access_not_found: The platform found no app hello in your current context."}, nil},
		{"expired login", `{"error":{"code":"unauthorized","message":"Sign in to continue."}}`, 401, false,
			[]string{"unauthorized: Sign in to continue.", "Run `latere login` and try again"}, nil},
		{"a body the platform could not read", `{"error":{"code":"invalid_field","message":"The request could not be read.","details":{"detail":"the body is {\"password\": \"...\"}"}}}`, 400, false,
			[]string{"invalid_field: The request could not be read.\ndetail: the body is {\"password\": \"...\"}"}, nil},
		{"a refusal of the password that names no bounds", `{"error":{"code":"invalid_field","message":"The request could not be read.","details":{"field":"password","reason":"length"}}}`, 400, false,
			[]string{"invalid_field: The platform refused the password. The app's password was not changed."}, nil},
		{"sharing down", `{"error":{"code":"apps_unavailable","message":"Sharing could not be reached. Try again shortly.","details":{"detail":"write the password failed"}}}`, 503, false,
			[]string{"apps_unavailable: Sharing could not be reached. Try again shortly.\ndetail: write the password failed"}, nil},
		// An answer that is not the platform's own may repeat the request,
		// so none of its text is printed.
		{"a page that repeats the request", `<html>502 Bad Gateway: PUT {"password":"` + piped + `"}</html>`, 502, false,
			[]string{"the platform answered HTTP 502"}, []string{"Bad Gateway", "html"}},
		{"an object without a code that repeats the request", `{"message":"upstream refused {\"password\":\"` + piped + `\"}","detail":"` + piped + `"}`, 502, false,
			[]string{"the platform answered HTTP 502"}, []string{"upstream", "detail"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := privateStub(t)
			if tc.atTheRead {
				s.accessStatus, s.accessBody = tc.status, tc.body
			} else {
				s.passwordStatus, s.passwordBody = tc.status, tc.body
			}
			out, errOut, err := runAppIn(t, s, t.TempDir(), piped+"\n", "password", "hello", "--stdin")
			if err == nil {
				t.Fatal("want a refusal")
			}
			wantContains(t, err.Error(), tc.want...)
			wantLacks(t, err.Error(), tc.lacks...)
			nowhere(t, piped, err, out, errOut)
			nowhere(t, "harbor", err, out, errOut)
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if tc.atTheRead && len(s.passwords) != 0 {
				t.Errorf("sets of the password = %d, want none", len(s.passwords))
			}
			if code := HandleExitError(&bytes.Buffer{}, err); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

// A change to private that is refused sends no password.
func TestAppPasswordOfAnAppThatCannotBeMadePrivate(t *testing.T) {
	s := newStubApps(t)
	s.apps["hello"] = stubAppAs("hello", "unlisted", "personal")
	s.patchStatus = 403
	s.patchBody = `{"error":{"code":"forbidden","message":"You do not have permission to do this.","details":{"action":"app.settings","reason":"not_owner"}}}`
	out, _, err := runApp(t, s, "password", "hello", "--generate")
	if err == nil || !strings.Contains(err.Error(), "forbidden: You may not change who can open hello (not_owner).") {
		t.Fatalf("password = %v", err)
	}
	if len(s.passwords) != 0 || out != "" {
		t.Errorf("sets of the password = %d, stdout = %q, want neither", len(s.passwords), out)
	}
}

func TestAppPasswordReadsTheSlugFromTheRemote(t *testing.T) {
	s := privateStub(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	out, _, err := runAppIn(t, s, dir, "harbor wall at low tide\n", "password", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "app:        hello\n", "The password of hello is set.\n")
	if got := s.passwordSent(t); got != "harbor wall at low tide" {
		t.Errorf("the platform was sent %q", got)
	}
}

func TestAppPasswordOfAnUnknownApp(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/gone.git")
	_, _, err := runAppIn(t, s, dir, "", "password", "--generate")
	if err == nil {
		t.Fatal("want app_not_found")
	}
	wantContains(t, err.Error(), "app_not_found", "The slug gone was read from the git remote latere.")
	if len(s.seenAtThePlatform()) != 0 {
		t.Errorf("requests at the platform = %v, want none", s.seenAtThePlatform())
	}
}

// --generate comes before --stdin, so standard input is not what is set
// when both are given.
func TestAppPasswordGenerateComesBeforeStandardInput(t *testing.T) {
	s := privateStub(t)
	out, _, err := runAppIn(t, s, t.TempDir(), "harbor wall at low tide\n", "password", "hello", "--stdin", "--generate")
	if err != nil {
		t.Fatal(err)
	}
	if sent := s.passwordSent(t); !generatedShape.MatchString(sent) || !strings.HasSuffix(out, "\n"+sent+"\n") {
		t.Errorf("the platform was sent %q, want the generated password the output ends with", sent)
	}
	nowhere(t, "harbor", nil, out)
}

// ---- remove ----

func TestAppPasswordRemove(t *testing.T) {
	for _, tc := range []struct {
		name, visibility, access, want string
		order                          []string
	}{
		{"a password that is set", "private", stubAccessWithPassword(),
			"Removed the password of hello. The people who used it no longer get in, and a visit made with it ends within about a minute.\n" + stubInvitedChoice,
			[]string{stubReadApp, stubReadAccess, stubDropPassword}},
		{"none set", "private", stubAccessJSON(),
			"hello has no password, so there was none to remove.\n" + stubInvitedChoice,
			[]string{stubReadApp, stubReadAccess}},
		{"one left on an app anyone with the link opens", "unlisted", stubAccessWithPassword(),
			"Removed the password of hello. Anyone with the link opens the app, where a password decides nothing, and one left set would open the app again once it is private.\n" +
				"Anyone with the link: it opens without signing in, and is kept out of search engines.\n",
			[]string{stubReadApp, stubReadAccess, stubDropPassword}},
		{"none set on an app anyone with the link opens", "unlisted", stubAccessJSON(),
			"hello has no password, so there was none to remove.\nAnyone with the link: it opens without signing in, and is kept out of search engines.\n",
			[]string{stubReadApp, stubReadAccess}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubApps(t)
			s.apps["hello"] = stubAppAs("hello", tc.visibility, "personal")
			s.access = tc.access
			out, errOut, err := runApp(t, s, "password", "hello", "--remove")
			if err != nil {
				t.Fatal(err)
			}
			if out != tc.want || errOut != "" {
				t.Errorf("password --remove =\n%s\nstderr = %q", out, errOut)
			}
			// The visibility is the app's own and a removal leaves it.
			if got := s.inOrder(); !slices.Equal(got, tc.order) {
				t.Errorf("requests in order = %v", got)
			}
			eachAPIItsOwnAudience(t, s)
		})
	}
}

func TestAppPasswordRemoveRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
	}{
		{"the platform stopped setting passwords", `{"error":{"code":"passwords_unconfigured","message":"Passwords for apps are not available yet.","details":{"detail":"PLATFORM_APP_PASSWORDS is not on"}}}`, 404,
			[]string{"passwords_unconfigured: Passwords are not available on this platform yet.\ndetail: PLATFORM_APP_PASSWORDS is not on"}},
		{"sharing down", `{"error":{"code":"apps_unavailable","message":"Sharing could not be reached. Try again shortly.","details":{"detail":"remove the password failed"}}}`, 503,
			[]string{"apps_unavailable: Sharing could not be reached. Try again shortly.\ndetail: remove the password failed"}},
		{"expired login", `{"error":{"code":"unauthorized","message":"Sign in to continue."}}`, 401,
			[]string{"unauthorized: Sign in to continue.", "Run `latere login` and try again"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := privateStub(t)
			s.access = stubAccessWithPassword()
			s.passwordRemoveStatus, s.passwordRemoveBody = tc.status, tc.body
			out, _, err := runApp(t, s, "password", "hello", "--remove")
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

// --remove sets nothing, so it takes no password, and says so before any
// request.
func TestAppPasswordRemoveTakesNoPassword(t *testing.T) {
	for _, flag := range []string{"--generate", "--stdin"} {
		s := privateStub(t)
		out, _, err := runApp(t, s, "password", "hello", "--remove", flag)
		if err == nil || !strings.Contains(err.Error(), "--remove sets no password, so it takes neither --generate nor --stdin") {
			t.Errorf("password --remove %s = %v", flag, err)
		}
		if len(s.inOrder()) != 0 || out != "" {
			t.Errorf("requests = %v, stdout = %q, want neither", s.inOrder(), out)
		}
	}
}

// ---- --json ----

// --json carries a password only when the command made it, and then once.
func TestAppPasswordJSON(t *testing.T) {
	type result struct {
		App      map[string]any `json:"app"`
		Password map[string]any `json:"password"`
	}
	run := func(t *testing.T, s *stubApps, stdin string, args ...string) (result, string) {
		t.Helper()
		out, errOut, err := runAppIn(t, s, t.TempDir(), stdin, append([]string{"password", "hello", "--json"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		if errOut != "" {
			t.Errorf("stderr = %q, want nothing", errOut)
		}
		var got result
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: %q", err, out)
		}
		return got, out
	}
	t.Run("a generated password", func(t *testing.T) {
		s := newStubApps(t)
		s.apps["hello"] = stubAppAs("hello", "unlisted", "personal")
		got, out := run(t, s, "", "--generate")
		sent := s.passwordSent(t)
		if len(got.Password) != 2 || got.Password["set"] != true || got.Password["generated"] != sent || !generatedShape.MatchString(sent) {
			t.Errorf("password = %v, want it set with the generated value the platform was sent", got.Password)
		}
		if n := strings.Count(out, sent); n != 1 {
			t.Errorf("the generated password is printed %d times, want once", n)
		}
		// The app is the answer of the change that made it private.
		if got.App["visibility"] != "private" || got.App["slug"] != "hello" || got.App["cluster"] == nil {
			t.Errorf("app = %v", got.App)
		}
	})
	t.Run("a piped password", func(t *testing.T) {
		s := privateStub(t)
		got, out := run(t, s, "harbor wall at low tide\n", "--stdin")
		if len(got.Password) != 1 || got.Password["set"] != true {
			t.Errorf("password = %v, want it set and nothing else", got.Password)
		}
		nowhere(t, "harbor", nil, out)
		if got.App["visibility"] != "private" || got.App["cluster"] == nil {
			t.Errorf("app = %v", got.App)
		}
	})
	t.Run("a typed password", func(t *testing.T) {
		s := privateStub(t)
		typedAtATerminal(t, "harbor wall at low tide", "harbor wall at low tide")
		out, errOut, err := runApp(t, s, "password", "hello", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var got result
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: %q", err, out)
		}
		if len(got.Password) != 1 || got.Password["set"] != true {
			t.Errorf("password = %v, want it set and nothing else", got.Password)
		}
		nowhere(t, "harbor", nil, out, errOut)
	})
	for name, access := range map[string]string{"a removed password": stubAccessWithPassword(), "no password to remove": stubAccessJSON()} {
		t.Run(name, func(t *testing.T) {
			s := privateStub(t)
			s.access = access
			got, _ := run(t, s, "", "--remove")
			if len(got.Password) != 1 || got.Password["set"] != false || got.App["slug"] != "hello" {
				t.Errorf("json = %+v, want the app and no password", got)
			}
		})
	}
}

// ---- help ----

func TestAppPasswordHelp(t *testing.T) {
	out, err := executeForHelp(NewRoot("test"), "apps", "password", "--help")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "A generated password is printed once, on the last line of the output.",
		"A password is never a flag's value or an argument", "--generate", "--stdin", "--remove", "--platform-url",
		"latere apps password hello --stdin < password.txt", "latere apps password hello --remove",
		"Without [slug], the slug is\nread from the git remote latere.")
	// The help describes a generated password in words, and they are the
	// shape the generator makes.
	if passwordGroups != 4 {
		t.Errorf("the help says four groups, and the generator makes %d", passwordGroups)
	}
	wantContains(t, out, fmt.Sprintf("%d letters and digits in four groups", passwordGroups*passwordGroupLen))
	// The words of the mechanism are the platform's, not a person's.
	wantLacks(t, strings.ToLower(out), "hash", "proof", "version")
}
