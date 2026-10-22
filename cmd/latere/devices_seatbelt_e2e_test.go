// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// The file builds on macOS alone, so the Linux run of the real sandbox,
// which fails on any skipped device test, never sees it.

//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	cellaclient "latere.ai/x/cella/client"
	"latere.ai/x/pkg/authkit/issuertest"
	"latere.ai/x/pkg/authz/stub"
)

// TestDevicesSessionCommandsRunUnderSeatbeltEndToEnd is the confinement of
// a session's command on macOS, end to end: the built latere binary
// connects this computer against a real cellad with nothing but /bin and
// /usr/bin on its PATH, so no srt and no ripgrep, and every command a
// session runs is confined by macOS's own Seatbelt through hostsandbox's
// native driver. The command reads its granted folder and is refused
// outside it; it cannot write a file the person's own tools later run as
// code, which only the native profile protects (.envrc); and its network
// is the launcher's proxy, which admits an allowed host and refuses
// another, while a connection around the proxy is refused by the sandbox.
// The allowed host does not resolve, so the proof needs no network: the
// proxy admits it and answers that it could not reach it.
func TestDevicesSessionCommandsRunUnderSeatbeltEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("the end-to-end run builds cellad, which -short leaves out")
	}
	issuer := issuertest.New(t)
	authorizer := stub.New(t)
	cellad := startCellad(t, issuer.URL(), authorizer.URL(), authorizer.Token())
	alice := issuer.Mint(issuertest.Claims{Sub: "alice", Aud: issuertest.StringList{"cella"}})
	cella, err := cellaclient.New(cellaclient.Config{URL: cellad, Token: cellaclient.StaticToken(alice)})
	if err != nil {
		t.Fatal(err)
	}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/account/environments/context":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tenant":{"labels":{"platform.latere.ai/tenant":"p-alice","platform.latere.ai/principal":"alice"}}}`)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/account/devices/"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(platform.Close)

	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("/tmp", "le")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	project := filepath.Join(home, "project")
	if err = os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(project, "granted.txt"), []byte("in the folder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "outside.txt"), []byte("not granted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".config")
	tokenFile := filepath.Join(config, "latere", "auth-token.json")
	if err = os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
		t.Fatal(err)
	}
	login, err := json.Marshal(map[string]any{"access_token": unsignedLogin("alice"), "token_type": "Bearer",
		"expires_at": time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(tokenFile, login, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state,
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "LATERE_AUTH_TOKEN_FILE=" + tokenFile,
		"LATERE_ENVIRONMENTS_URL=" + cellad, "LATERE_ENVIRONMENTS_TOKEN=" + alice,
		"LATERE_PLATFORM_URL=" + platform.URL, "LATERE_PLATFORM_TOKEN=platform-bearer",
		"LATERE_DEVICE_KEYS_FILE=" + filepath.Join(config, "latere", "device-keys.json"),
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true",
		// Nothing installed: a PATH with neither srt nor ripgrep, which only
		// the native driver can serve.
		"PATH=/bin:/usr/bin", "TMPDIR=" + os.TempDir(),
	}

	connect := exec.Command(latereBinary(t), "devices", "connect", "--folder", project,
		"--host", "allowed.invalid", "--name", "Seatbelt computer")
	connect.Env = env
	stdin, err := connect.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	connect.Stdout, connect.Stderr = &stderr, &stderr
	if err = connect.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- connect.Wait() }()
	t.Cleanup(func() {
		_ = connect.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = connect.Process.Kill()
		}
		if t.Failed() {
			t.Logf("latere devices connect:\n%s", stderr.String())
		}
	})
	if _, err = io.WriteString(stdin, "yes\n"); err != nil {
		t.Fatal(err)
	}
	waitForText(t, &stderr, "Online.", exited)
	id := regexp.MustCompile(`dev-[a-z2-7]{16}`).FindString(stderr.String())
	if id == "" {
		t.Fatalf("connect named no device:\n%s", stderr.String())
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		environment, _, err := cella.GetEnvironment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if environment.Status.Workers == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the environment never counted the worker: %+v", environment.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	manifest := `{"apiVersion":"cella.latere.ai/v1beta1","kind":"Sandbox","metadata":{"name":"seatbelt-sandbox",` +
		`"labels":{"topos.latere.ai/session":"ses_seatbelt","topos.latere.ai/agent":"agt_e2e","topos.latere.ai/agent-version":"1"},` +
		`"annotations":{"topos.latere.ai/agent-name":"E2E helper"}},` +
		`"spec":{"environment":"` + id + `","workspace":{"host":"` + project + `"},` +
		`"network":{"egress":{"mode":"allowlist","allowedHosts":["allowed.invalid"]}}}}`
	sb, _, err := cella.CreateSandbox(context.Background(), cellaclient.JSON([]byte(manifest)), cellaclient.Wait(time.Minute))
	if err != nil {
		t.Fatalf("the create failed: %v", err)
	}
	run := func(script string) cellaclient.ExecResult {
		t.Helper()
		result, _, err := cella.Exec(context.Background(), sb.Status.ID, cellaclient.ExecRequest{Command: []string{"/bin/sh", "-c", script}})
		if err != nil {
			t.Fatalf("the exec of %q answered %v", script, err)
		}
		return result
	}

	if r := run("cat granted.txt"); r.ExitCode != 0 || r.Stdout != "in the folder\n" {
		t.Errorf("reading the granted folder answered %+v", r)
	}
	if r := run("cat " + filepath.Join(home, "outside.txt")); r.ExitCode == 0 || strings.Contains(r.Stdout, "not granted") {
		t.Errorf("reading outside the folder answered %+v, want it refused", r)
	}
	if r := run("echo x > " + filepath.Join(home, "escape.txt")); r.ExitCode == 0 {
		t.Errorf("writing outside the folder answered %+v, want it refused", r)
	}
	if _, err := os.Stat(filepath.Join(home, "escape.txt")); !os.IsNotExist(err) {
		t.Errorf("a file landed outside the folder: %v", err)
	}
	if r := run("echo 'export X=1' > .envrc"); r.ExitCode == 0 {
		t.Errorf("writing .envrc in the folder answered %+v, want it refused", r)
	}
	if r := run("echo written > note.txt && cat note.txt"); r.ExitCode != 0 || r.Stdout != "written\n" {
		t.Errorf("writing an ordinary file in the folder answered %+v", r)
	}

	// The proxy admits the allowed host and cannot reach it, which is its
	// 502; it refuses any other host with its 403; and a connection that
	// skips the proxy never leaves the sandbox.
	if r := run("/usr/bin/curl -q -sS --max-time 20 -o /dev/null -w '%{http_code}' http://allowed.invalid/"); r.Stdout != "502" {
		t.Errorf("the allowed host answered %+v, want the proxy's 502 after admitting it", r)
	}
	// The proxy's refusal is its 403, after the line it writes into the
	// command's output naming the host it refused.
	if r := run("/usr/bin/curl -q -sS --max-time 20 -o /dev/null -w '%{http_code}' http://denied.invalid/"); !strings.HasSuffix(r.Stdout, "403") ||
		!strings.Contains(r.Stdout, "refused a connection to denied.invalid:80") {
		t.Errorf("a host not allowed answered %+v, want the proxy's 403 and its refusal named", r)
	}
	if r := run("/usr/bin/curl -q -sS --max-time 10 --noproxy '*' -o /dev/null http://1.1.1.1/"); r.ExitCode == 0 {
		t.Errorf("a connection around the proxy answered %+v, want it refused by the sandbox", r)
	}

	// The refused host reaches the terminal serving the computer as one
	// line naming the session, the host and the port, with the two
	// commands that allow it.
	waitForText(t, &stderr, "Session ses_seatbelt (agent agt_e2e) was refused a connection to denied.invalid:80.", exited)
	waitForText(t, &stderr, "latere devices allow denied.invalid:80 --session ses_seatbelt", exited)
	waitForText(t, &stderr, "latere devices allow denied.invalid:80 --save", exited)
	latere := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(latereBinary(t), args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("latere %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	before, err := os.ReadFile(filepath.Join(config, "latere", "device.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Allowed for the session, its next command reaches the host, which
	// the proxy now admits and cannot resolve, and the file is unchanged.
	latere("devices", "allow", "denied.invalid:80", "--session", "ses_seatbelt")
	if r := run("/usr/bin/curl -q -sS --max-time 20 -o /dev/null -w '%{http_code}' http://denied.invalid/"); r.Stdout != "502" {
		t.Errorf("a host allowed for the session answered %+v, want the proxy's 502 after admitting it", r)
	}
	if after, err := os.ReadFile(filepath.Join(config, "latere", "device.yaml")); err != nil || string(after) != string(before) {
		t.Errorf("allowing a host for one session changed the host policy file: %v\n%s", err, after)
	}
	// Saved, it joins the allowlist in the file, and every session's next
	// command reaches it without a restart.
	latere("devices", "allow", "saved.invalid", "--save")
	if r := run("/usr/bin/curl -q -sS --max-time 20 -o /dev/null -w '%{http_code}' http://saved.invalid/"); r.Stdout != "502" {
		t.Errorf("a saved host answered %+v, want the proxy's 502 after admitting it", r)
	}
	if after, err := os.ReadFile(filepath.Join(config, "latere", "device.yaml")); err != nil || !strings.Contains(string(after), "hosts: [allowed.invalid, saved.invalid]") {
		t.Errorf("the host policy file after the save reads %v\n%s", err, after)
	}
	activity := latere("devices", "activity")
	for _, want := range []string{"ses_seatbelt  network_refused  denied.invalid:80", "ses_seatbelt  host_allowed  denied.invalid:80", "host_saved  saved.invalid:443"} {
		if !strings.Contains(activity, want) {
			t.Errorf("the activity lacks %q:\n%s", want, activity)
		}
	}
}

// TestDevicesNetworkAnyUnderSeatbeltEndToEnd is the network any as the
// built binary checks it on macOS: its main runs hostsandbox's launcher, so
// macOS's own sandbox enforces any and the host policy takes it, after the
// sentence that says what it lets out, where a binary without the launcher
// refuses it as unenforceable.
func TestDevicesNetworkAnyUnderSeatbeltEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("/tmp", "le")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	if err = os.MkdirAll(filepath.Join(home, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".config")
	policy := filepath.Join(config, "latere", "device.yaml")
	if err = os.MkdirAll(filepath.Dir(policy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(policy, []byte("device: dev-abcdefghijklmnop\nname: Seatbelt computer\nsession_start: allow\n"+
		"folders:\n  - path: ~/project\n    write: true\nnetwork:\n  mode: allowlist\n  hosts: [github.com]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state, "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"LATERE_AUTH_TOKEN_FILE=" + filepath.Join(config, "latere", "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1",
		"OTEL_SDK_DISABLED=true", "PATH=/bin:/usr/bin", "TMPDIR=" + os.TempDir(),
	}
	latere := func(args ...string) (string, error) {
		cmd := exec.Command(latereBinary(t), args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := latere("devices", "policy", "--network", "any", "--block", "blocked.invalid", "--yes")
	if err != nil || !strings.Contains(out, "Everything readable in a granted folder can be sent to any public host.") ||
		!strings.Contains(out, "Saved the network any") || !strings.Contains(out, "The host policy file is valid.") {
		t.Fatalf("policy --network any --yes: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(policy)
	if err != nil || !strings.Contains(string(raw), "mode: any") || !strings.Contains(string(raw), "blocked: [blocked.invalid]") {
		t.Errorf("the host policy file reads %v\n%s", err, raw)
	}
	if out, err = latere("devices", "policy"); err != nil || !strings.Contains(out, "Network: any.") || !strings.Contains(out, "blocked.invalid") {
		t.Errorf("policy over any: %v\n%s", err, out)
	}
}
