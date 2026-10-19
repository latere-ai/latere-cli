// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/devices"
	"github.com/latere-ai/latere-cli/internal/upgrade"
)

// deviceHome is a person's computer in temporary directories: the home, the
// configuration, the state (short, for the control socket) and the cache,
// with a project folder to share. It answers the engine's paths.
func deviceHome(t *testing.T) (devices.Paths, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("/tmp", "lc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(home, ".config", "latere", "auth-token.json"))
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	project := filepath.Join(home, "project")
	if err = os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := devices.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	return paths, project
}

// sandboxStandIn puts a stand-in for srt on PATH where the real one is not
// installed, so the engine's driver passes its preflight on every runner.
// These tests prove the command's flow; confinement is internal/devices'.
func sandboxStandIn(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("srt"); err == nil {
		return
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "srt"), []byte("#!/bin/sh\nshift 3\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rg", "bwrap", "socat"} {
		if err := os.Symlink("/bin/echo", filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/bin:/usr/bin")
}

// signIn saves a login for the person sub, in the organization org when it
// is not empty.
func signIn(t *testing.T, org string) {
	t.Helper()
	claims := map[string]any{"sub": "p1", "email": "p1@example.com"}
	if org != "" {
		claims["org_id"] = org
	}
	if err := api.SaveAuthToken(api.Token{AccessToken: fakeJWT(t, claims), TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

// devicePlatform is the platform and its Cella core for the CLI: the labels
// read, the device routes, and the environment routes under
// /v1/environments. Every request is recorded.
type devicePlatform struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []string
	// bearers is each request's route with the bearer it presented.
	bearers []string
	created []byte
	keyBody string
	renamed map[string]string
	removed []string
	devices []map[string]any
	// refuseCreate, when set, answers the create with this detail.
	refuseCreate string
	// register answers a worker's registration: by default the stub stops
	// the worker with worker_replaced, so a connect ends once it serves.
	register func(w http.ResponseWriter)
}

func newDevicePlatform(t *testing.T) *devicePlatform {
	t.Helper()
	p := &devicePlatform{renamed: map[string]string{}}
	answer := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/account/environments/context", func(w http.ResponseWriter, r *http.Request) {
		answer(w, http.StatusOK, map[string]any{"tenant": map[string]any{"labels": map[string]string{
			"platform.latere.ai/tenant": "p-0123", "platform.latere.ai/principal": "p1",
		}}})
	})
	mux.HandleFunc("GET /v1/account/devices", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		items := p.devices
		if items == nil {
			items = []map[string]any{}
		}
		answer(w, http.StatusOK, map[string]any{"items": items, "limit": 10})
	})
	mux.HandleFunc("PATCH /v1/account/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.renamed[r.PathValue("id")] = body["name"]
		p.mu.Unlock()
		answer(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "name": body["name"]})
	})
	mux.HandleFunc("DELETE /v1/account/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.removed = append(p.removed, r.PathValue("id"))
		p.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/environments/environments", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.created = body
		refuse := p.refuseCreate
		p.mu.Unlock()
		if refuse != "" {
			answer(w, http.StatusForbidden, map[string]any{"error": map[string]any{
				"code": "forbidden", "message": "You do not have permission to do this.", "details": map[string]any{"detail": refuse},
			}})
			return
		}
		var env map[string]any
		_ = json.Unmarshal(body, &env)
		answer(w, http.StatusCreated, env)
	})
	mux.HandleFunc("POST /v1/environments/environments/{id}/keys", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.keyBody = string(body)
		p.mu.Unlock()
		answer(w, http.StatusCreated, map[string]any{"token": "ENVIRONMENT-KEY-SECRET", "jti": "jti-1", "exp": time.Now().Add(720 * time.Hour)})
	})
	mux.HandleFunc("POST /v1/environments/environments/{id}/workers", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		register := p.register
		p.mu.Unlock()
		if register != nil {
			register(w)
			return
		}
		answer(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "worker_replaced", "details": map[string]any{"detail": "the stub stops here"}}})
	})
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.Method+" "+r.URL.Path)
		p.bearers = append(p.bearers, r.Method+" "+r.URL.Path+" "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		p.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	t.Setenv("LATERE_ENVIRONMENTS_URL", p.srv.URL+"/v1/environments")
	t.Setenv("LATERE_ENVIRONMENTS_TOKEN", "cella-bearer")
	t.Setenv("LATERE_PLATFORM_URL", p.srv.URL)
	t.Setenv("LATERE_PLATFORM_TOKEN", "platform-bearer")
	return p
}

func (p *devicePlatform) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

// runDevices runs the CLI with stdin, and answers its stdout, its stderr
// and its error.
func runDevices(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestConnectRefusesRoot(t *testing.T) {
	deviceHome(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	previous := deviceEUID
	deviceEUID = func() int { return 0 }
	t.Cleanup(func() { deviceEUID = previous })
	_, _, err := runDevices(t, "yes\n", "devices", "connect")
	if devices.CodeOf(err) != devices.CodeRunsAsRoot {
		t.Fatalf("connect as root returned %v", err)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("connect as root reached the platform: %v", got)
	}
}

func TestConnectRefusesUnverifiedBuild(t *testing.T) {
	deviceHome(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	t.Cleanup(upgrade.SetReleaseKeysForTest())
	_, _, err := runDevices(t, "yes\n", "devices", "connect")
	if devices.CodeOf(err) != devices.CodeUnverifiedBuild {
		t.Fatalf("connect on a build with no release key returned %v", err)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("an unverified build reached the platform: %v", got)
	}
}

func TestConnectRefusesOrgContext(t *testing.T) {
	deviceHome(t)
	p := newDevicePlatform(t)
	signIn(t, "org-1")
	_, _, err := runDevices(t, "yes\n", "devices", "connect")
	if devices.CodeOf(err) != devices.CodeOrgContext || !strings.Contains(err.Error(), "latere org --personal") {
		t.Fatalf("connect in an organization's context returned %v", err)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("connect in an organization's context reached the platform: %v", got)
	}
}

func TestConnectNeedsASignIn(t *testing.T) {
	deviceHome(t)
	newDevicePlatform(t)
	_, _, err := runDevices(t, "yes\n", "devices", "connect")
	if devices.CodeOf(err) != devices.CodeNoSignIn || !strings.Contains(err.Error(), "latere login") {
		t.Fatalf("connect with no login returned %v", err)
	}
}

func TestConnectScreenNeedsYes(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	for _, answer := range []string{"no\n", "y\n", ""} {
		_, errOut, err := runDevices(t, answer, "devices", "connect", "--folder", project, "--name", "Test Mac")
		if devices.CodeOf(err) != devices.CodeNotConfirmed {
			t.Fatalf("connect answered %q returned %v", answer, err)
		}
		for _, want := range []string{
			`"Test Mac"`, "~/project (read-write)", "readable to the agent", ".env", "github.com, codeload.github.com",
			"can carry data out", "start on\nthis computer without asking here", "still approved in the chat",
			"--session-start ask", "session's log", "Type yes",
		} {
			if !strings.Contains(errOut, want) {
				t.Errorf("the connect screen lacks %q:\n%s", want, errOut)
			}
		}
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("something was created before a typed yes: %v", got)
	}
	if _, err := os.Stat(paths.PolicyFile()); err == nil {
		t.Error("the host policy file was written before a typed yes")
	}
}

func TestConnectCreatesTheEnvironment(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	if err := os.MkdirAll(filepath.Join(project, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runDevices(t, "yes\n", "devices", "connect", "--folder", project, "--folder", project+"/sub:ro",
		"--name", "Test Mac", "--host", "github.com")
	if devices.CodeOf(err) != devices.CodeWorkerReplaced {
		t.Fatalf("connect returned %v; stderr:\n%s", err, errOut)
	}
	p.mu.Lock()
	created, keyBody, renamed := p.created, p.keyBody, p.renamed
	p.mu.Unlock()
	var env struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Mode      string `json:"mode"`
			Isolation string `json:"isolation"`
			Workers   struct {
				Max int `json:"max"`
			} `json:"workers"`
			Capacity struct {
				Sandboxes int `json:"sandboxes"`
			} `json:"capacity"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(created, &env); err != nil {
		t.Fatalf("the create body %s: %v", created, err)
	}
	id := env.Metadata.Name
	if !regexp.MustCompile(`^dev-[a-z2-7]{16}$`).MatchString(id) || env.Kind != "Environment" || env.Spec.Mode != "worker" ||
		env.Spec.Isolation != "process" || env.Spec.Workers.Max != 1 || env.Spec.Capacity.Sandboxes != deviceSandboxes {
		t.Errorf("the create was %s", created)
	}
	for k, v := range map[string]string{"platform.latere.ai/tenant": "p-0123", "platform.latere.ai/principal": "p1", "platform.latere.ai/kind": "device"} {
		if env.Metadata.Labels[k] != v {
			t.Errorf("the create's label %s is %q, want %q", k, env.Metadata.Labels[k], v)
		}
	}
	if renamed[id] != "Test Mac" {
		t.Errorf("the device was named %v", renamed)
	}
	if !strings.Contains(keyBody, `"ttl":"720h0m0s"`) {
		t.Errorf("the key was minted with %q, want a ttl of 30 days", keyBody)
	}
	key, ok, err := devices.OpenKeys(paths.KeyFile()).Get(id)
	if err != nil || !ok || key.Token != "ENVIRONMENT-KEY-SECRET" || key.JTI != "jti-1" {
		t.Errorf("the keychain holds %+v, %v, %v", key, ok, err)
	}
	if _, err := os.Stat(paths.KeyFile()); err == nil {
		t.Error("a key kept in a working keychain also landed in the file")
	}
	policy, _, err := devices.LoadPolicy(paths.PolicyFile())
	if err != nil || policy.Device != id || policy.Name != "Test Mac" || policy.SessionStart != devices.SessionStartAllow || len(policy.Folders) != 2 ||
		policy.Folders[0].Path != "~/project" || !policy.Folders[0].Write || policy.Folders[1].Write ||
		strings.Join(policy.Network.Hosts, ",") != "github.com" {
		t.Errorf("the host policy file reads %+v, %v", policy, err)
	}
	if info, err := os.Stat(paths.PolicyFile()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the host policy file is %v, %v", info, err)
	}
	logs := out + errOut
	if raw, err := os.ReadFile(filepath.Join(paths.ActivityDir(id), "activity.ndjson")); err == nil {
		logs += string(raw)
	}
	if strings.Contains(logs, "ENVIRONMENT-KEY-SECRET") {
		t.Error("the environment key was printed or logged")
	}
	if !strings.Contains(errOut, "Connected this computer as Test Mac ("+id+")") {
		t.Errorf("connect did not say it connected:\n%s", errOut)
	}
	// The saved login's person owns the device, so an application that
	// serves it later refuses another person's tokens.
	var owner struct{ Device, Subject string }
	if raw, err := os.ReadFile(paths.OwnerFile()); err != nil || json.Unmarshal(raw, &owner) != nil || owner.Device != id || owner.Subject != "p1" {
		t.Errorf("the owner recorded at connect is %+v, %v", owner, err)
	}
}

func TestSecondConnectRefusedLocally(t *testing.T) {
	paths, _ := deviceHome(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	lock, err := devices.TakeLock(paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	_, _, err = runDevices(t, "yes\n", "devices", "connect")
	if devices.CodeOf(err) != devices.CodeAlreadyServed || !strings.Contains(err.Error(), "already being served") {
		t.Fatalf("a second connect returned %v", err)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("a second connect reached the platform: %v", got)
	}
}

func TestConnectedComputerServesWithoutTheScreen(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	connectedHere(t, paths, project)
	_, errOut, err := runDevices(t, "", "devices", "connect")
	if devices.CodeOf(err) != devices.CodeWorkerReplaced {
		t.Fatalf("connect returned %v", err)
	}
	if strings.Contains(errOut, "Type yes") {
		t.Errorf("a connected computer was shown the connect screen:\n%s", errOut)
	}
	for _, r := range p.seen() {
		if strings.HasSuffix(r, "/environments/environments") {
			t.Errorf("a connected computer created another environment: %v", p.seen())
		}
	}
	if _, _, err := runDevices(t, "", "devices", "connect", "--folder", project); devices.CodeOf(err) != devices.CodeAlreadyConnected {
		t.Errorf("flags on a connected computer returned %v", err)
	}
	if _, _, err := runDevices(t, "", "devices", "serve"); devices.CodeOf(err) != devices.CodeWorkerReplaced {
		t.Errorf("serve returned %v", err)
	}
}

// connectedHere writes the host policy of a connected computer and keeps a
// key for it.
func connectedHere(t *testing.T, paths devices.Paths, project string) string {
	t.Helper()
	const id = "dev-abcdefghijklmnop"
	if err := devices.SavePolicy(paths.PolicyFile(), devices.Policy{
		Device: id, Name: "Test Mac", Folders: []devices.Folder{{Path: project, Write: true}},
		Network: devices.Network{Mode: devices.NetworkNone},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := devices.OpenKeys(paths.KeyFile()).Put(devices.Key{Environment: id, Token: "k", JTI: "j", MintedAt: now, ExpiresAt: now.Add(devices.KeyTTL)}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConnectRefusalsFromThePlatform(t *testing.T) {
	for _, tc := range []struct {
		detail, code, hint string
	}{
		{"forbidden: environment.create: device_limit", devices.CodeLimit, "You have 2 devices, and one person may connect 10."},
		{"forbidden: environment.create: device_personal_only", devices.CodePersonalOnly, "latere org --personal"},
		{"forbidden: environment.create: sign_in_required", devices.CodeSignInRequired, "latere login"},
		{"forbidden: environment.create: device_key_refused", devices.CodeKeyRefused, "latere login"},
		{"forbidden: environment.create: device_invalid", devices.CodeInvalid, ""},
		{"forbidden: environment.create: Something Else", devices.CodeRefused, ""},
	} {
		t.Run(tc.code, func(t *testing.T) {
			_, project := deviceHome(t)
			sandboxStandIn(t)
			p := newDevicePlatform(t)
			p.refuseCreate = tc.detail
			p.devices = []map[string]any{{"id": "dev-a"}, {"id": "dev-b"}}
			signIn(t, "")
			_, _, err := runDevices(t, "yes\n", "devices", "connect", "--folder", project)
			if devices.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.hint) {
				t.Errorf("a create refused with %q returned %v", tc.detail, err)
			}
		})
	}
}

func TestDevicesList(t *testing.T) {
	paths, project := deviceHome(t)
	p := newDevicePlatform(t)
	id := connectedHere(t, paths, project)
	seen := time.Now().Add(-3 * time.Minute)
	p.devices = []map[string]any{
		{"id": id, "name": "Test Mac", "status": "online", "os": "darwin", "arch": "arm64", "last_seen": seen,
			"folders": []map[string]any{{"path": "/Users/p/site", "write": true}, {"path": "/Users/p/notes"}}},
		{"id": "dev-zzzzzzzzzzzzzzzz", "name": "Desktop", "status": "offline", "folders": []map[string]any{}},
	}
	out, _, err := runDevices(t, "", "devices", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "*  Test Mac", id, "darwin/arm64", "online", "3m ago", "/Users/p/site (rw), /Users/p/notes (ro)",
		"Desktop", "offline", "* this computer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list lacks %q:\n%s", want, out)
		}
	}
	out, _, err = runDevices(t, "", "devices", "list", "--json")
	if err != nil || !strings.Contains(out, `"limit": 10`) {
		t.Errorf("the JSON list is %s, %v", out, err)
	}
	p.devices = nil
	if out, _, err = runDevices(t, "", "devices", "list"); err != nil || !strings.Contains(out, "You have no devices") {
		t.Errorf("an empty list reads %q, %v", out, err)
	}
}

func TestDevicesRenameAndRemove(t *testing.T) {
	paths, project := deviceHome(t)
	p := newDevicePlatform(t)
	id := connectedHere(t, paths, project)
	if _, _, err := runDevices(t, "", "devices", "rename", "New Name"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runDevices(t, "", "devices", "rename", "Other", "dev-zzzzzzzzzzzzzzzz"); err != nil {
		t.Fatal(err)
	}
	policy, _, _ := devices.LoadPolicy(paths.PolicyFile())
	if p.renamed[id] != "New Name" || p.renamed["dev-zzzzzzzzzzzzzzzz"] != "Other" || policy.Name != "New Name" {
		t.Errorf("renamed %v, and the file names %q", p.renamed, policy.Name)
	}
	if _, _, err := runDevices(t, "", "devices", "remove", "dev-zzzzzzzzzzzzzzzz"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := devices.OpenKeys(paths.KeyFile()).Get(id); !ok {
		t.Error("removing another device deleted this computer's key")
	}
	if _, _, err := runDevices(t, "", "devices", "remove"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.removed, ",") != "dev-zzzzzzzzzzzzzzzz,"+id {
		t.Errorf("removed %v", p.removed)
	}
	if _, ok, _ := devices.OpenKeys(paths.KeyFile()).Get(id); ok {
		t.Error("removing this computer kept its key")
	}
	if _, err := os.Stat(paths.PolicyFile()); err == nil {
		t.Error("removing this computer kept its host policy file")
	}
	for _, args := range [][]string{{"devices", "remove"}, {"devices", "rename", "x"}, {"devices", "activity"}} {
		if _, _, err := runDevices(t, "", args...); devices.CodeOf(err) != devices.CodeNotConnected {
			t.Errorf("%v on a computer not connected returned %v", args, err)
		}
	}
}

func TestDevicesPolicy(t *testing.T) {
	paths, project := deviceHome(t)
	if _, _, err := runDevices(t, "", "devices", "policy"); devices.CodeOf(err) != devices.CodeNotConnected {
		t.Errorf("policy with no file returned %v", err)
	}
	connectedHere(t, paths, project)
	policy, _, _ := devices.LoadPolicy(paths.PolicyFile())
	policy.Remember("trg_1", "4", time.Now())
	policy.RememberedTriggers = append(policy.RememberedTriggers, devices.Remembered{Trigger: "trg_old", AgentVersion: "1", Until: time.Now().Add(-time.Hour)})
	if err := devices.SavePolicy(paths.PolicyFile(), policy); err != nil {
		t.Fatal(err)
	}
	out, _, err := runDevices(t, "", "devices", "policy")
	if err != nil {
		t.Fatalf("policy returned %v:\n%s", err, out)
	}
	for _, want := range []string{paths.PolicyFile(), "device: dev-abcdefghijklmnop", "trg_1  agent version 4  until", "trg_old  agent version 1  expired", "The host policy file is valid."} {
		if !strings.Contains(out, want) {
			t.Errorf("policy lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "start on this computer without asking here") {
		t.Errorf("policy does not say how sessions start:\n%s", out)
	}
	// --session-start writes the file, which the next session reads.
	_, errOut, err := runDevices(t, "", "devices", "policy", "--session-start", "ask")
	if err != nil || !strings.Contains(errOut, "session_start is ask from the next session on.") {
		t.Fatalf("policy --session-start ask returned %v:\n%s", err, errOut)
	}
	if policy, _, _ = devices.LoadPolicy(paths.PolicyFile()); policy.SessionStart != devices.SessionStartAsk || !policy.Remembers("trg_1", "4", time.Now()) {
		t.Errorf("after --session-start ask the file reads %+v", policy)
	}
	if out, _, _ = runDevices(t, "", "devices", "policy"); !strings.Contains(out, "Each session asks for your approval here") {
		t.Errorf("policy under ask does not say so:\n%s", out)
	}
	if _, _, err = runDevices(t, "", "devices", "policy", "--session-start", "sometimes"); devices.CodeOf(err) != devices.CodePolicyInvalid {
		t.Errorf("policy --session-start sometimes returned %v", err)
	}
	if policy, _, _ = devices.LoadPolicy(paths.PolicyFile()); policy.SessionStart != devices.SessionStartAsk {
		t.Errorf("a refused --session-start changed the file: %+v", policy)
	}
	if _, _, err = runDevices(t, "", "devices", "policy", "--forget", "trg_1"); err != nil {
		t.Fatal(err)
	}
	policy, _, _ = devices.LoadPolicy(paths.PolicyFile())
	if policy.Remembers("trg_1", "4", time.Now()) {
		t.Error("a forgotten trigger is still remembered")
	}
	if _, _, err = runDevices(t, "", "devices", "policy", "--forget", "trg_none"); devices.CodeOf(err) != devices.CodePolicyInvalid {
		t.Errorf("forgetting an unknown trigger returned %v", err)
	}
	policy.Folders = append(policy.Folders, devices.Folder{Path: paths.Home})
	if err := devices.SavePolicy(paths.PolicyFile(), policy); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runDevices(t, "", "devices", "policy"); devices.CodeOf(err) != devices.CodePolicyInvalid || !strings.Contains(err.Error(), "home directory") {
		t.Errorf("a policy granting the home directory returned %v", err)
	}
}

func TestDevicesActivity(t *testing.T) {
	paths, project := deviceHome(t)
	id := connectedHere(t, paths, project)
	log, err := devices.OpenActivity(paths.ActivityDir(id))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, e := range []devices.Entry{
		{Time: old, Type: "Exec", Session: "ses_old", Command: []string{"make"}},
		{Time: time.Now(), Type: "Write", Session: "ses_new", Path: filepath.Join(paths.Home, "project", "a.go")},
	} {
		if err = log.Write(e); err != nil {
			t.Fatal(err)
		}
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	out, _, err := runDevices(t, "", "devices", "activity")
	if err != nil || !strings.Contains(out, "ses_old  exec  make") || !strings.Contains(out, "ses_new  write  ~/project/a.go") {
		t.Errorf("activity printed %q, %v", out, err)
	}
	out, _, err = runDevices(t, "", "devices", "activity", "--since", "1h", "--json")
	if err != nil || strings.Contains(out, "ses_old") || !strings.Contains(out, `"session":"ses_new"`) {
		t.Errorf("activity since an hour printed %q, %v", out, err)
	}
	if _, _, err = runDevices(t, "", "devices", "activity", "--since", "soon"); err == nil {
		t.Error("a bad --since was taken")
	}
}

func TestPlatformReason(t *testing.T) {
	for detail, want := range map[string]string{
		"forbidden: environment.create: device_limit": "device_limit",
		"no reason":                         "",
		"forbidden: environment.create: Oh": "",
		"forbidden: x: ":                    "",
	} {
		if got := platformReason(detail); got != want {
			t.Errorf("platformReason(%q) = %q, want %q", detail, got, want)
		}
	}
}

func TestCleanDeviceName(t *testing.T) {
	long := strings.Repeat("é", 70)
	for in, want := range map[string]string{
		"  MacBook Pro  ": "MacBook Pro",
		"a\x00b\u202ec":   "abc",
		"":                "This computer",
		long:              strings.Repeat("é", 64),
	} {
		if got := devices.CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
	if name := defaultComputerName(); name == "" {
		t.Error("the computer has no name")
	}
}

func TestConnectReadsLabelsFromTheFlaggedPlatform(t *testing.T) {
	_, project := deviceHome(t)
	sandboxStandIn(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	t.Setenv("LATERE_PLATFORM_URL", "http://127.0.0.1:1")
	_, errOut, err := runDevices(t, "yes\n", "devices", "connect", "--folder", project, "--platform-url", p.srv.URL)
	if devices.CodeOf(err) != devices.CodeWorkerReplaced {
		t.Fatalf("connect with --platform-url returned %v:\n%s", err, errOut)
	}
	labels, renamed := false, false
	for _, r := range p.seen() {
		labels = labels || r == "GET /v1/account/environments/context"
		renamed = renamed || strings.HasPrefix(r, "PATCH /v1/account/devices/")
	}
	if !labels || !renamed {
		t.Errorf("the flagged platform saw %v; want the labels read and the name set there", p.seen())
	}
}
