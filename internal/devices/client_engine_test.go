// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package devices

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"latere.ai/x/cella/runtime"
	"latere.ai/x/cella/runtime/local"
)

// What a desktop client relies on of the engine beyond spec 018's routes:
// a rename that leaves running sessions alone, tokens tied to the person
// whose device this is, a key that waits for the client's sign-in, a
// consent that covers every grant applied, a status a client can follow,
// and a removal that leaves nothing behind.

// tokenFor is an unsigned JWT for the audience, the person sub and, when
// set, an organization's context.
func tokenFor(audience, sub, org string, exp time.Time) string {
	part := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	claims := map[string]any{"sub": sub, "aud": audience, "exp": exp.Unix()}
	if org != "" {
		claims["org_id"] = org
	}
	return part(map[string]any{"alg": "none"}) + "." + part(claims) + ".sig"
}

// tokensFor is a PUT /v1/credentials body for the person sub.
func tokensFor(sub string) string {
	exp := time.Now().Add(time.Hour)
	return fmt.Sprintf(`{"cella":{"token":%q},"platform":{"token":%q}}`, tokenFor(AudienceCella, sub, "", exp), tokenFor(AudiencePlatform, sub, "", exp))
}

// tokenAccount is the person's side of the control plane reached with a
// client's tokens: a mint first takes the Cella token, as the command's
// account does.
type tokenAccount struct {
	*fakeAccount
	creds *Credentials
}

func (a tokenAccount) MintKey(ctx context.Context, env string, ttl time.Duration) (Key, error) {
	if _, err := a.creds.Token(ctx, AudienceCella); err != nil {
		return Key{}, err
	}
	return a.fakeAccount.MintKey(ctx, env, ttl)
}

func TestRenameLeavesRunningSessionsAlone(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	if err := os.MkdirAll(filepath.Join(paths.Home, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := newStubPlane(t)
	var mu sync.Mutex
	builds := 0
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: &fakeEnroller{paths: paths},
		NewDriver: func(o local.Options) (runtime.Driver, error) {
			if !strings.Contains(o.DataDir, "check-") {
				mu.Lock()
				builds++
				mu.Unlock()
			}
			return local.New(o)
		}})
	c := controlOn(t, e, paths)
	running(t, e)
	p.waitConnected(t)
	served := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return builds, len(p.bearersNow())
	}
	beforeBuilds, beforeRegistrations := served()

	for _, body := range []string{`{"name":"Work laptop"}`, `{}`, `{"folders":[{"path":"~/project","write":true}]}`} {
		if code, out := send(t, c, http.MethodPut, "/v1/policy", body); code != http.StatusOK {
			t.Fatalf("PUT /v1/policy %s answered %d %s", body, code, out)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if b, r := served(); b != beforeBuilds || r != beforeRegistrations {
		t.Errorf("a rename, an empty change and the same folders restarted the worker: %d builds and %d registrations, was %d and %d", b, r, beforeBuilds, beforeRegistrations)
	}
	if s := e.Status(); s.Name != "Work laptop" {
		t.Errorf("the status names %q", s.Name)
	}

	// A change of the folders is served, which restarts the worker.
	if code, out := send(t, c, http.MethodPut, "/v1/policy", `{"folders":[{"path":"~/other","write":false}]}`); code != http.StatusOK {
		t.Fatalf("a change of the folders answered %d %s", code, out)
	}
	waitFor(t, "the new folder to be served", func() bool { b, _ := served(); return b > beforeBuilds })
}

func TestRenameRunsOutsideTheFileLock(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	p := newStubPlane(t)
	enroll := &fakeEnroller{paths: paths, renameStarted: make(chan struct{}), renameGo: make(chan struct{})}
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: enroll})
	running(t, e)
	p.waitConnected(t)

	answered := make(chan error, 1)
	go func() {
		_, err := e.PutPolicy(context.Background(), PolicyChange{Name: new("Slow rename")})
		answered <- err
	}()
	<-enroll.renameStarted
	// A session start reads the file while the platform is still renaming.
	read := make(chan bool, 1)
	go func() {
		_, current := e.livePolicy(testEnv)
		read <- current
	}()
	select {
	case current := <-read:
		if !current {
			t.Error("the file did not read as this device's during the rename")
		}
	case <-time.After(2 * time.Second):
		t.Error("a session start waited for the platform's rename")
	}
	close(enroll.renameGo)
	if err := <-answered; err != nil {
		t.Fatalf("the rename answered %v", err)
	}
	if file, _, _ := LoadPolicy(paths.PolicyFile()); file.Name != "Slow rename" {
		t.Errorf("the file names %q", file.Name)
	}
}

func TestTokensAreTiedToTheDevicesOwner(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	t.Run("a token in an organization's context is refused", func(t *testing.T) {
		creds := NewCredentials(nil, 0)
		var ce *CredentialsError
		err := creds.Put(map[string]HeldToken{AudienceCella: {Token: tokenFor(AudienceCella, "alice", "org_1", exp)}})
		if !errors.As(err, &ce) || ce.Code != CodeCredentialsOrg {
			t.Errorf("an organization's token answered %v", err)
		}
	})
	t.Run("tokens of two people handed together are refused", func(t *testing.T) {
		creds := NewCredentials(nil, 0)
		err := creds.Put(map[string]HeldToken{
			AudienceCella:    {Token: tokenFor(AudienceCella, "alice", "", exp)},
			AudiencePlatform: {Token: tokenFor(AudiencePlatform, "bob", "", exp)},
		})
		var ce *CredentialsError
		if !errors.As(err, &ce) {
			t.Errorf("tokens of two people answered %v", err)
		}
	})
	t.Run("unbound, any person's tokens are taken, and another person's replace them", func(t *testing.T) {
		creds := NewCredentials(nil, 0)
		if err := creds.Put(map[string]HeldToken{AudienceCella: {Token: tokenFor(AudienceCella, "alice", "", exp)}, AudiencePlatform: {Token: tokenFor(AudiencePlatform, "alice", "", exp)}}); err != nil {
			t.Fatal(err)
		}
		if err := creds.Put(map[string]HeldToken{AudienceCella: {Token: tokenFor(AudienceCella, "bob", "", exp)}}); err != nil {
			t.Fatalf("a computer not connected refused another person's token: %v", err)
		}
		if creds.Subject() != "bob" || creds.State().Platform != CredentialNeeded {
			t.Errorf("alice's platform token stands beside bob's: subject %q, %+v", creds.Subject(), creds.State())
		}
	})

	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	if err := writeOwner(paths.OwnerFile(), testEnv, "alice"); err != nil {
		t.Fatal(err)
	}
	creds := NewCredentials(nil, 200*time.Millisecond)
	e := New(Config{Paths: paths, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: &fakeEnroller{paths: paths}, Credentials: creds, WaitToConnect: true})
	c := controlOn(t, e, paths)

	t.Run("another person's tokens are refused, and the device is not forgotten", func(t *testing.T) {
		code, body := send(t, c, http.MethodPut, "/v1/credentials", tokensFor("bob"))
		if code != http.StatusConflict || errorCode(body) != CodeCredentialsOtherPerson {
			t.Errorf("bob's tokens answered %d %s", code, body)
		}
		if file, ok, err := LoadPolicy(paths.PolicyFile()); err != nil || !ok || file.Device != testEnv {
			t.Errorf("the device was forgotten: %+v, %v, %v", file, ok, err)
		}
		if _, ok, err := keys.Get(testEnv); !ok || err != nil {
			t.Errorf("the key was deleted: %v, %v", ok, err)
		}
	})
	t.Run("the owner's tokens are taken", func(t *testing.T) {
		if code, body := send(t, c, http.MethodPut, "/v1/credentials", tokensFor("alice")); code != http.StatusOK {
			t.Errorf("alice's tokens answered %d %s", code, body)
		}
	})
	t.Run("removed here, the next connect may be anyone's", func(t *testing.T) {
		if code, body := post(t, c, "/v1/remove", `{"local":true}`); code != http.StatusOK {
			t.Fatalf("the local removal answered %d %s", code, body)
		}
		if _, err := os.Stat(paths.OwnerFile()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the record of the owner outlived the device: %v", err)
		}
		if code, body := send(t, c, http.MethodPut, "/v1/credentials", tokensFor("bob")); code != http.StatusOK {
			t.Errorf("bob's tokens on a computer not connected answered %d %s", code, body)
		}
	})
}

func TestOwnerIsLearnedFromTheFirstTokens(t *testing.T) {
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	creds := NewCredentials(nil, 0)
	e := New(Config{Paths: paths, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Credentials: creds})
	c := controlOn(t, e, paths)
	if code, body := send(t, c, http.MethodPut, "/v1/credentials", tokensFor("alice")); code != http.StatusOK {
		t.Fatalf("the first tokens answered %d %s", code, body)
	}
	if owner, err := readOwner(paths.OwnerFile(), testEnv); err != nil || owner != "alice" {
		t.Errorf("the owner recorded is %q, %v", owner, err)
	}
	if code, body := send(t, c, http.MethodPut, "/v1/credentials", tokensFor("bob")); code != http.StatusConflict || errorCode(body) != CodeCredentialsOtherPerson {
		t.Errorf("another person's tokens answered %d %s", code, body)
	}
}

func TestClientModeWaitsForTheKey(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	if err := keys.Delete(testEnv); err != nil {
		t.Fatal(err)
	}
	p := newStubPlane(t)
	creds := NewCredentials(nil, 100*time.Millisecond)
	account := tokenAccount{fakeAccount: &fakeAccount{exists: true}, creds: creds}
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: account, Version: "test", Credentials: creds, WaitToConnect: true})
	c := controlOn(t, e, paths)
	_, done := running(t, e)
	waitFor(t, "the engine to wait for the sign-in", func() bool { s := e.Status(); return s.State == StateOffline && strings.Contains(s.Detail, "sign-in") })
	time.Sleep(4 * creds.wait)
	select {
	case <-done:
		t.Fatal("serve ended while the key waited for the client's sign-in")
	default:
	}
	if s := statusOf(t, c); s.Credentials == nil || s.Credentials.Cella != CredentialNeeded {
		t.Errorf("the socket's status reads %+v", s.Credentials)
	}
	if code, body := send(t, c, http.MethodPut, "/v1/credentials", tokensFor("alice")); code != http.StatusOK {
		t.Fatalf("the tokens answered %d %s", code, body)
	}
	p.waitConnected(t)
	waitFor(t, "the device to be online", func() bool { return e.Status().Online })
}

func TestConsentCoversWhatTheFileKept(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	for _, dir := range []string{"project", "notes"} {
		if err := os.MkdirAll(filepath.Join(paths.Home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := SavePolicy(paths.PolicyFile(), Policy{
		Name: "Test computer", Folders: []Folder{{Path: "~/project", Write: true}},
		Network: Network{Mode: NetworkAllowlist, Hosts: []string{"github.com"}}, ReadPaths: []string{"~/notes"},
	}); err != nil {
		t.Fatal(err)
	}
	p := newStubPlane(t)
	enroll := &fakeEnroller{paths: paths}
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: enroll, WaitToConnect: true})
	c := controlOn(t, e, paths)
	running(t, e)
	waitFor(t, "the state not_connected", func() bool { return e.Status().State == StateNotConnected })

	code, body := get(t, c, "/v1/connect?name=Test+computer&folder=~/project")
	var proposal Proposal
	if code != http.StatusOK || json.Unmarshal([]byte(body), &proposal) != nil {
		t.Fatalf("the proposal answered %d %s", code, body)
	}
	var kept *Fact
	for i := range proposal.Facts {
		if proposal.Facts[i].Kind == FactReadPaths {
			kept = &proposal.Facts[i]
		}
	}
	if kept == nil || len(kept.Items) != 1 || kept.Items[0] != "~/notes" {
		t.Fatalf("the facts do not name the read path the file keeps: %+v", proposal.Facts)
	}
	// The file grants another path between the proposal and the connect.
	file, _, _ := LoadPolicy(paths.PolicyFile())
	file.ReadPaths = append(file.ReadPaths, "~/project")
	if err := SavePolicy(paths.PolicyFile(), file); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]any{"policy": proposal.Policy, "consent": proposal.Consent})
	if code, body := post(t, c, "/v1/connect", string(request)); code != http.StatusUnprocessableEntity || errorCode(body) != CodeNotConfirmed {
		t.Errorf("a connect past a changed file answered %d %s", code, body)
	}
	if len(enroll.connects) != 0 {
		t.Errorf("a stale consent connected: %v", enroll.connects)
	}
}

func TestConnectScreenUnchangedWithoutKeptGrants(t *testing.T) {
	for i, want := range []string{
		"Connect this computer to Latere as \"Mac\"?\n\nSessions you start in Latere will run commands and read and edit files\non this computer, in these folders only:\n  ~/a (read-write)\n  ~/b (read-only)\nEverything in these folders is readable to the agent, a project's .env\nfile and other secrets kept there included.\n\nNetwork: commands may reach these hosts only:\n  github.com, pypi.org\nA host that accepts writes, such as a git host, can carry data out.\n\nOnly sessions you start, and triggers you own, can use this computer.\nSessions you start, and your triggers that run on a schedule, start on\nthis computer without asking here. A trigger that runs on mail or a\nwebhook asks here first, since anyone with its address can send it input.\nEach command that changes something is still approved in the chat,\nunless you approved a matching one before.\nAnyone who can start sessions as you can reach these folders, still\nconfined to them: never your credentials, keychains or browser profiles,\nand no network beyond what is listed above.\nTo be asked here before every session starts, run\n`latere devices policy --session-start ask`.\n\nWhat the agent reads on this computer goes to the session's log and to\nthe model. In an organization's conversation, that organization can\nread it too.\n\nType yes to connect this computer: ",
		"Connect this computer to Latere as \"Mac\"?\n\nSessions you start in Latere will run commands and read and edit files\non this computer, in these folders only:\n  ~/a (read-only)\nEverything in these folders is readable to the agent, a project's .env\nfile and other secrets kept there included.\n\nNetwork: none. Commands on this computer reach no host.\n\nOnly sessions you start, and triggers you own, can use this computer.\nEach session asks for your approval here before it starts. Each command\nthat changes something is also approved in the chat, unless you approved\na matching one before.\nTo let your own sessions and scheduled triggers start without asking\nhere, run `latere devices policy --session-start allow`.\n\nWhat the agent reads on this computer goes to the session's log and to\nthe model. In an organization's conversation, that organization can\nread it too.\n\nType yes to connect this computer: ",
	} {
		p := []Policy{
			{Name: "Mac", Folders: []Folder{{Path: "~/a", Write: true}, {Path: "~/b"}}, Network: Network{Mode: NetworkAllowlist, Hosts: []string{"github.com", "pypi.org"}}},
			{Name: "Mac", SessionStart: SessionStartAsk, Folders: []Folder{{Path: "~/a"}}, Network: Network{Mode: NetworkNone}},
		}[i]
		if got := ConnectScreen(p); got != want {
			t.Errorf("the connect screen changed:\n%s\nwant:\n%s", got, want)
		}
	}
	kept := Policy{Name: "Mac", Folders: []Folder{{Path: "~/a"}}, Network: Network{Mode: NetworkNone}, ReadPaths: []string{"~/.gitconfig"},
		RememberedTriggers: []Remembered{{Trigger: "trg_daily", AgentVersion: "4", Until: time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)}}}
	screen := ConnectScreen(kept)
	for _, want := range []string{"~/.gitconfig", "trg_daily (agent version 4) until 18 November 2026"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen does not tell what the file keeps, %q:\n%s", want, screen)
		}
	}
}

func TestStatusCanBeFollowed(t *testing.T) {
	paths := testPaths(t)
	creds := NewCredentials(nil, 0)
	e := New(Config{Paths: paths, Keys: OpenKeys(paths.KeyFile()), Account: &fakeAccount{}, Credentials: creds})
	c := controlOn(t, e, paths)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://device/v1/status?follow=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	statuses := make(chan Status, 16)
	go func() {
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			var s Status
			if json.Unmarshal(scanner.Bytes(), &s) == nil {
				statuses <- s
			}
		}
		close(statuses)
	}()
	next := func(what string, cond func(Status) bool) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case s, ok := <-statuses:
				if !ok {
					t.Fatalf("the stream ended before %s", what)
				}
				if s.Credentials != nil && cond(s) {
					return
				}
			case <-deadline:
				t.Fatalf("the stream never said %s", what)
			}
		}
	}
	next("the token is needed", func(s Status) bool { return s.Credentials.Platform == CredentialNeeded })
	exp := time.Now().Add(credentialsMargin + time.Second)
	body := fmt.Sprintf(`{"platform":{"token":%q}}`, tokenFor(AudiencePlatform, "alice", "", exp))
	if code, out := send(t, c, http.MethodPut, "/v1/credentials", body); code != http.StatusOK {
		t.Fatalf("the token answered %d %s", code, out)
	}
	next("the token is valid", func(s Status) bool { return s.Credentials.Platform == CredentialValid })
	next("the token is needed again", func(s Status) bool { return s.Credentials.Platform == CredentialNeeded })
}

func TestConnectAnswersARefusedNameAndCleansIt(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	if err := os.MkdirAll(filepath.Join(paths.Home, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := newStubPlane(t)
	enroll := &fakeEnroller{paths: paths, nameErr: errors.New("the name is taken")}
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: enroll, WaitToConnect: true})
	c := controlOn(t, e, paths)
	running(t, e)
	waitFor(t, "the state not_connected", func() bool { return e.Status().State == StateNotConnected })
	code, body := get(t, c, "/v1/connect?name=Test+computer&folder=~/project")
	var proposal Proposal
	if code != http.StatusOK || json.Unmarshal([]byte(body), &proposal) != nil {
		t.Fatalf("the proposal answered %d %s", code, body)
	}
	posted := proposal.Policy
	posted.Name = "  Test computer \n"
	request, _ := json.Marshal(map[string]any{"policy": posted, "consent": proposal.Consent})
	code, body = post(t, c, "/v1/connect", string(request))
	var answer ConnectAnswer
	if code != http.StatusOK || json.Unmarshal([]byte(body), &answer) != nil {
		t.Fatalf("the connect answered %d %s", code, body)
	}
	if len(answer.Warnings) != 1 || !strings.Contains(answer.Warnings[0], "was not set") || !strings.Contains(answer.Warnings[0], "the name is taken") {
		t.Errorf("the answer warns %v", answer.Warnings)
	}
	if file, _, _ := LoadPolicy(paths.PolicyFile()); file.Name != "Test computer" {
		t.Errorf("the file names %q", file.Name)
	}
}

func TestAMissingTokenIsSaidSo(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	p := newStubPlane(t)
	needed := fmt.Errorf("the platform call: %w", ErrCredentialsNeeded)
	enroll := &fakeEnroller{paths: paths, renameErr: needed, removeErr: needed}
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: enroll})
	c := controlOn(t, e, paths)
	running(t, e)
	p.waitConnected(t)
	if code, body := send(t, c, http.MethodPut, "/v1/policy", `{"name":"Elsewhere"}`); code != http.StatusUnprocessableEntity || errorCode(body) != CodeCredentialsNeeded {
		t.Errorf("a rename that lacked the token answered %d %s", code, body)
	}
	if code, body := post(t, c, "/v1/remove", ""); code != http.StatusUnprocessableEntity || errorCode(body) != CodeCredentialsNeeded {
		t.Errorf("a removal that lacked the token answered %d %s", code, body)
	}
}

// cwdEnroller proposes the folder it runs in when none is asked, as the
// command does from a terminal.
type cwdEnroller struct{ *fakeEnroller }

func (c cwdEnroller) Propose(previous Policy, q ConnectQuery) (Policy, error) {
	p, err := c.fakeEnroller.Propose(previous, q)
	if err == nil && len(p.Folders) == 0 {
		p.Folders = []Folder{{Path: "/", Write: true}}
	}
	return p, err
}

func TestAProposalWithNoFolderAsksForOne(t *testing.T) {
	paths := testPaths(t)
	e := New(Config{Paths: paths, Keys: OpenKeys(paths.KeyFile()), Account: &fakeAccount{}, Enroll: cwdEnroller{&fakeEnroller{paths: paths}}, WaitToConnect: true})
	c := controlOn(t, e, paths)
	code, body := get(t, c, "/v1/connect?name=Test+computer")
	var proposal Proposal
	if code != http.StatusOK || json.Unmarshal([]byte(body), &proposal) != nil {
		t.Fatalf("the proposal answered %d %s", code, body)
	}
	if len(proposal.Policy.Folders) != 0 || len(proposal.Problems) == 0 || !strings.Contains(strings.Join(proposal.Problems, " "), "folders is empty") {
		t.Errorf("a proposal with no folder reads %+v", proposal)
	}
}

func TestRemovalWhilePausedDeletesTheSandboxes(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	project := connected(t, paths, keys)
	p := newStubPlane(t)
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: &fakeEnroller{paths: paths}, WaitToConnect: true})
	answering(t, e, AnswerAllow)
	c := controlOn(t, e, paths)
	running(t, e)
	p.waitConnected(t)
	if _, err := p.driver.Create(t.Context(), deviceCreate("sbx_p", project, sessionLabels("ses_p"))); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(paths.DataDir(testEnv)); err != nil || len(entries) == 0 {
		t.Fatalf("the sandbox left nothing to delete: %v, %v", entries, err)
	}
	if code, body := post(t, c, "/v1/pause", ""); code != http.StatusOK {
		t.Fatalf("the pause answered %d %s", code, body)
	}
	waitFor(t, "the pause", func() bool { return e.Status().State == StatePaused })
	if code, body := post(t, c, "/v1/remove", `{"local":true}`); code != http.StatusOK {
		t.Fatalf("the removal answered %d %s", code, body)
	}
	if _, err := os.Stat(paths.DataDir(testEnv)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sandboxes of a device removed while paused are still kept: %v", err)
	}
}

func TestRemovalClearsTheKeyToRevoke(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	p := newStubPlane(t)
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: &fakeAccount{exists: true}, Version: "test", Enroll: &fakeEnroller{paths: paths}, WaitToConnect: true})
	c := controlOn(t, e, paths)
	running(t, e)
	p.waitConnected(t)
	e.mu.Lock()
	e.retiring = &Key{Environment: testEnv, JTI: "jti-old"}
	e.mu.Unlock()
	if code, body := post(t, c, "/v1/remove", `{"local":true}`); code != http.StatusOK {
		t.Fatalf("the removal answered %d %s", code, body)
	}
	e.mu.Lock()
	retiring := e.retiring
	e.mu.Unlock()
	if retiring != nil {
		t.Errorf("a removed device's key is still to be revoked after the next registration: %+v", retiring)
	}
}

func TestAStartAfterARemovalServesNothing(t *testing.T) {
	sandboxRuntime(t)
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	policy, _, err := LoadPolicy(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	account := &fakeAccount{exists: true}
	p := newStubPlane(t)
	e := New(Config{Paths: paths, URL: p.url, Keys: keys, Account: account, Version: "test", WaitToConnect: true})
	// A removal through the socket forgot the device after the run read the
	// file and before it started.
	if err := Forget(keys, paths.PolicyFile(), testEnv, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.serveDevice(ctx, policy); !errors.Is(err, errClientRemoved) {
		t.Errorf("a start after the removal answered %v", err)
	}
	if len(p.bearersNow()) != 0 {
		t.Errorf("the removed device registered: %v", p.bearersNow())
	}
}

func TestRemovalDeletesAFileTheWorkerAlreadyCleared(t *testing.T) {
	paths := testPaths(t)
	keys := OpenKeys(paths.KeyFile())
	connected(t, paths, keys)
	// The worker saw the removal at the platform first: the file names no
	// device and keeps its folders.
	file, _, err := LoadPolicy(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	file.Device = ""
	if err := SavePolicy(paths.PolicyFile(), file); err != nil {
		t.Fatal(err)
	}
	e := New(Config{Paths: paths, Keys: keys, Account: &fakeAccount{}})
	if err := e.forgetHere(testEnv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.PolicyFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the host policy file outlived a removal through the socket: %v", err)
	}
}
