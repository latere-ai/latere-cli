// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	cellaclient "latere.ai/x/cella/client"
	v1 "latere.ai/x/cella/manifest/v1"
	"latere.ai/x/pkg/authkit/issuertest"
	"latere.ai/x/pkg/authz/stub"
)

// The end-to-end run of spec 016: the built latere binary connects this
// computer against a real cellad, with an embedded worker on the local
// driver, a stub authorizer that allows the owner, and a stub of the
// platform's device routes. A session's sandbox is created on a granted
// folder and starts with no one asked, as session_start allow has it, and
// runs a command that writes into the folder; switched to ask, the next
// session's sandbox is approved over the control socket; and a removal
// stops the command.
//
// Where srt is installed it confines the command; elsewhere, as on the CI
// runners, a stand-in runs it unconfined, so the whole flow is proved on
// every runner and the confinement by internal/devices' tests where srt is.

// cellaVersion is the Cella module this build pins, whose cellad the test
// runs, so the control plane and the embedded worker are one release.
func cellaVersion(t *testing.T) string {
	t.Helper()
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("the test binary carries no build information")
	}
	for _, dep := range info.Deps {
		if dep.Path == "latere.ai/x/cella" {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	t.Fatal("this build does not depend on latere.ai/x/cella")
	return ""
}

// celladBinary builds cellad at the pinned version into a directory of its
// own, outside this module, so its dependencies are its own.
func celladBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	install := exec.Command("go", "install", "latere.ai/x/cella/cmd/cellad@"+cellaVersion(t))
	install.Dir = dir
	install.Env = append(os.Environ(), "GOBIN="+dir, "GOFLAGS=", "GOWORK=off")
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("build cellad: %v\n%s", err, out)
	}
	return filepath.Join(dir, "cellad")
}

// signingKeyPEM is a fresh RSA key in PEM, which cellad signs its workload
// tokens and environment keys with.
func signingKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// syncBuffer is an output a process writes while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var celladListening = regexp.MustCompile(`listening public=(\S+) internal=(\S+)`)

// startCellad runs cellad with the issuer and the authorizer, and answers
// its public URL.
func startCellad(t *testing.T, issuer, authorizer, authorizerToken string) string {
	t.Helper()
	binary := celladBinary(t)
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"CELLA_OIDC_ISSUERS="+issuer,
		"CELLA_RUNTIME=native", "CELLA_ALLOW_UNSAFE_NATIVE=true",
		"CELLA_PUBLIC_URL=https://control.example.com",
		"CELLA_TOKEN_KEY="+signingKeyPEM(t),
		"CELLA_DATA_DIR="+t.TempDir(),
		"CELLA_PUBLIC_ADDR=127.0.0.1:0", "CELLA_INTERNAL_ADDR=127.0.0.1:0",
		"CELLA_AUTHORIZER_URL="+authorizer, "CELLA_AUTHORIZER_TOKEN="+authorizerToken,
		"OTEL_SDK_DISABLED=true",
	)
	var out, errOut syncBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
		if t.Failed() {
			t.Logf("cellad stdout:\n%s\ncellad stderr:\n%s", out.String(), errOut.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if m := celladListening.FindStringSubmatch(out.String()); m != nil {
			return "http://" + m[1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("cellad never reported its listeners; stdout %q stderr %q", out.String(), errOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// standInPath is the PATH the latere binary runs with: the real srt where it
// is installed, and a stand-in that runs each command unconfined elsewhere.
func standInPath(t *testing.T) string {
	t.Helper()
	if srt, err := exec.LookPath("srt"); err == nil {
		rg, rgErr := exec.LookPath("rg")
		dirs := []string{filepath.Dir(srt)}
		if rgErr == nil {
			dirs = append(dirs, filepath.Dir(rg))
		}
		return strings.Join(append(dirs, "/bin", "/usr/bin"), ":")
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
	t.Log("srt is not installed here, so the device's commands run unconfined under a stand-in")
	return dir + ":/bin:/usr/bin"
}

// unsignedLogin is a saved login for the person, in their personal context.
// The CLI reads its claims and never verifies it; every product is reached
// with the bearers the environment hands it.
func unsignedLogin(sub string) string {
	part := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return part(map[string]any{"alg": "none"}) + "." + part(map[string]any{"sub": sub}) + ".sig"
}

func TestDevicesConnectEndToEnd(t *testing.T) {
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

	// The platform's device routes: the labels a create carries, the name,
	// and a removal that deletes the environment at Cella, as platformd's
	// service subject does.
	var mu sync.Mutex
	named := map[string]string{}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/account/environments/context":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tenant":{"labels":{"platform.latere.ai/tenant":"p-alice","platform.latere.ai/principal":"alice"}}}`)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/account/devices/"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			named[strings.TrimPrefix(r.URL.Path, "/v1/account/devices/")] = body["name"]
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/account/devices/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/account/devices/")
			if _, err := cella.Delete(r.Context(), cellaclient.KindEnvironment, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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
	config := filepath.Join(home, ".config")
	tokenFile := filepath.Join(config, "latere", "auth-token.json")
	if err = os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
		t.Fatal(err)
	}
	login, _ := json.Marshal(map[string]any{"access_token": unsignedLogin("alice"), "token_type": "Bearer",
		"expires_at": time.Now().Add(time.Hour)})
	if err = os.WriteFile(tokenFile, login, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state,
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "LATERE_AUTH_TOKEN_FILE=" + tokenFile,
		"LATERE_ENVIRONMENTS_URL=" + cellad, "LATERE_ENVIRONMENTS_TOKEN=" + alice,
		"LATERE_PLATFORM_URL=" + platform.URL, "LATERE_PLATFORM_TOKEN=platform-bearer",
		// The key is kept in a file: the operating system's keychain is the
		// person's, and no test reaches it.
		"LATERE_DEVICE_KEYS_FILE=" + filepath.Join(config, "latere", "device-keys.json"),
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "PATH=" + standInPath(t),
		"TMPDIR=" + os.TempDir(),
	}

	connect := exec.Command(latereBinary(t), "devices", "connect", "--folder", project, "--network", "none", "--name", "E2E computer")
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
	mu.Lock()
	name := named[id]
	mu.Unlock()
	if name != "E2E computer" {
		t.Errorf("the device was named %q", name)
	}
	// The engine reads online once the registration is taken; the
	// environment counts the worker once its stream is open.
	var environment v1.Environment
	deadline := time.Now().Add(30 * time.Second)
	for {
		if environment, _, err = cella.GetEnvironment(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if environment.Status.Workers == 1 && len(environment.Status.Hosts) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the environment never counted the worker: %+v", environment.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if environment.Spec.Mode != "worker" || environment.Spec.Workers.Max != 1 || environment.Status.Workers != 1 ||
		len(environment.Status.Hosts) != 1 || environment.Status.Hosts[0].Host.Grants[0].Path != project {
		t.Errorf("the environment reads %+v", environment)
	}

	// The person's own session starts with no one asked, under the default
	// session_start allow, and is told on the control socket and in the
	// terminal.
	socket := filepath.Join(state, "latere", "devices", "control.sock")
	approvals := followSocket(t, socket, "/v1/approvals?follow=1")
	manifest := func(name, session string) []byte {
		return fmt.Appendf(nil, `{"apiVersion":"cella.latere.ai/v1beta1","kind":"Sandbox","metadata":{"name":%q,`+
			`"labels":{"topos.latere.ai/session":%q,"topos.latere.ai/agent":"agt_e2e","topos.latere.ai/agent-version":"1"},`+
			`"annotations":{"topos.latere.ai/agent-name":"E2E helper"}},`+
			`"spec":{"environment":%q,"workspace":{"host":%q}}}`, name, session, id, project)
	}
	sb, _, err := cella.CreateSandbox(context.Background(), cellaclient.JSON(manifest("session-sandbox", "ses_e2e")), cellaclient.Wait(time.Minute))
	if err != nil {
		t.Fatalf("the create under session_start allow failed: %v", err)
	}
	sandbox := sb.Status.ID
	started := nextLine(t, approvals)
	if facts, _ := started["approval"].(map[string]any); started["kind"] != "started" || facts["agent"] != "E2E helper" ||
		facts["session"] != "ses_e2e" || facts["folder"] != project {
		t.Fatalf("the clients were told %v", started)
	}
	waitForText(t, &stderr, "E2E helper (ses_e2e, started by a person) is using ~/project (read-write).", exited)

	// Switched to ask, which applies to the next session with the computer
	// serving, a session's create waits for the person, who answers over
	// the control socket.
	policy := exec.Command(latereBinary(t), "devices", "policy", "--session-start", "ask")
	policy.Env = env
	if out, err := policy.CombinedOutput(); err != nil {
		t.Fatalf("policy --session-start ask: %v\n%s", err, out)
	}
	created := make(chan error, 1)
	go func() {
		_, _, err := cella.CreateSandbox(context.Background(), cellaclient.JSON(manifest("asked-sandbox", "ses_asked")), cellaclient.Wait(time.Minute))
		created <- err
	}()
	asked := nextLine(t, approvals)
	approval, _ := asked["approval"].(map[string]any)
	if asked["kind"] != "asked" || approval["agent"] != "E2E helper" || approval["session"] != "ses_asked" || approval["folder"] != project {
		t.Fatalf("the person was asked %v", asked)
	}
	answer(t, socket, approval["id"].(string), "allow")
	if err = <-created; err != nil {
		t.Fatalf("the approved create failed: %v", err)
	}
	result, _, err := cella.Exec(context.Background(), sandbox, cellaclient.ExecRequest{
		Command: []string{"/bin/sh", "-c", "echo from-the-session > note.txt && cat note.txt"},
	})
	if err != nil || result.ExitCode != 0 || result.Stdout != "from-the-session\n" {
		var ce *cellaclient.Error
		if errors.As(err, &ce) {
			t.Fatalf("the exec answered %s: %s", ce.Code, ce.Detail)
		}
		t.Fatalf("the exec answered %+v, %v", result, err)
	}
	if note, err := os.ReadFile(filepath.Join(project, "note.txt")); err != nil || string(note) != "from-the-session\n" {
		t.Errorf("the granted folder holds %q, %v", note, err)
	}
	// A command may start in a directory of the person's folder.
	if err = os.MkdirAll(filepath.Join(project, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, _, err = cella.Exec(context.Background(), sandbox, cellaclient.ExecRequest{
		Command: []string{"/bin/sh", "-c", "pwd"}, Workdir: filepath.Join(project, "sub"),
	})
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != filepath.Join(project, "sub") {
		t.Errorf("an exec in a directory of the folder answered %+v, %v", result, err)
	}
	waitForText(t, &stderr, "ses_e2e  exec  /bin/sh -c", exited)

	// Removing the device stops the command and forgets the device here.
	remove := exec.Command(latereBinary(t), "devices", "remove", id)
	remove.Env = env
	if out, err := remove.CombinedOutput(); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	select {
	case err := <-exited:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Errorf("connect ended with %v, want exit status 1", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("connect kept serving a removed device")
	}
	if !strings.Contains(stderr.String(), "device_removed: This computer was removed from your devices.") {
		t.Errorf("connect did not say the computer was removed:\n%s", stderr.String())
	}
}

// waitForText waits for text in a process's output, failing early when the
// process ends first.
func waitForText(t *testing.T, out *syncBuffer, text string, exited <-chan error) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for !strings.Contains(out.String(), text) {
		select {
		case err := <-exited:
			t.Fatalf("the process ended (%v) before printing %q:\n%s", err, text, out.String())
		case <-deadline:
			t.Fatalf("no %q within a minute:\n%s", text, out.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// socketClient speaks HTTP over the control socket.
func socketClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
}

// followSocket opens a stream of the local API.
func followSocket(t *testing.T, socket, route string) <-chan map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://device"+route, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := socketClient(socket).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan map[string]any, 16)
	go func() {
		defer func() { _ = res.Body.Close() }()
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			var v map[string]any
			if json.Unmarshal(scanner.Bytes(), &v) == nil {
				lines <- v
			}
		}
	}()
	return lines
}

func nextLine(t *testing.T, lines <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case v := <-lines:
		return v
	case <-time.After(60 * time.Second):
		t.Fatal("no line arrived on the control socket")
	}
	return nil
}

func answer(t *testing.T, socket, id, answer string) {
	t.Helper()
	res, err := socketClient(socket).Post("http://device/v1/approvals/"+id, "application/json",
		strings.NewReader(`{"answer":"`+answer+`","client":"the end-to-end test"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("the answer was refused: %s %s", res.Status, body)
	}
}

// The end-to-end run of spec 018: `latere devices serve --credentials
// client`, with no saved login at all, is connected, renamed and removed
// through its control socket alone, as a desktop client drives it, against
// a real cellad and the platform's device routes. Every bearer is a token
// the client handed: Cella's verifies it, and the platform stub reads
// none.
func TestDevicesServeThroughTheSocketEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("the end-to-end run builds cellad, which -short leaves out")
	}
	issuer := issuertest.New(t)
	authorizer := stub.New(t)
	cellad := startCellad(t, issuer.URL(), authorizer.URL(), authorizer.Token())
	exp := time.Now().Add(time.Hour)
	cellaToken := issuer.Mint(issuertest.Claims{Sub: "alice", Aud: issuertest.StringList{"cella"}, Exp: exp.Unix()})
	platformToken := issuer.Mint(issuertest.Claims{Sub: "alice", Aud: issuertest.StringList{"api.latere.ai"}, Exp: exp.Unix()})
	cella, err := cellaclient.New(cellaclient.Config{URL: cellad, Token: cellaclient.StaticToken(cellaToken)})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	named := map[string]string{}
	var bearers []string
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bearers = append(bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/account/environments/context":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tenant":{"labels":{"platform.latere.ai/tenant":"p-alice","platform.latere.ai/principal":"alice"}}}`)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/account/devices/"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			named[strings.TrimPrefix(r.URL.Path, "/v1/account/devices/")] = body["name"]
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/account/devices/"):
			if _, err := cella.Delete(r.Context(), cellaclient.KindEnvironment, strings.TrimPrefix(r.URL.Path, "/v1/account/devices/")); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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
	config := filepath.Join(home, ".config")
	env := []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state,
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		// No saved login exists, and no token is given in the environment:
		// every bearer is one the client hands through the socket.
		"LATERE_AUTH_TOKEN_FILE=" + filepath.Join(config, "latere", "auth-token.json"),
		"LATERE_ENVIRONMENTS_URL=" + cellad, "LATERE_PLATFORM_URL=" + platform.URL,
		"LATERE_DEVICE_KEYS_FILE=" + filepath.Join(config, "latere", "device-keys.json"),
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "PATH=" + standInPath(t),
		"TMPDIR=" + os.TempDir(),
	}
	serve := exec.Command(latereBinary(t), "devices", "serve", "--credentials", "client")
	serve.Env = env
	var stderr syncBuffer
	serve.Stdout, serve.Stderr = &stderr, &stderr
	if err = serve.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- serve.Wait() }()
	t.Cleanup(func() {
		_ = serve.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = serve.Process.Kill()
		}
		if t.Failed() {
			t.Logf("latere devices serve:\n%s", stderr.String())
		}
	})
	waitForText(t, &stderr, "Not connected.", exited)
	socket := filepath.Join(state, "latere", "devices", "control.sock")
	client := socketClient(socket)
	call := func(method, route string, body any) (int, map[string]any) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, "http://device"+route, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var answer map[string]any
		_ = json.NewDecoder(res.Body).Decode(&answer)
		return res.StatusCode, answer
	}
	waitState := func(want string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			_, status := call(http.MethodGet, "/v1/status", nil)
			if status["state"] == want {
				return status
			}
			if time.Now().After(deadline) {
				t.Fatalf("the state stayed %v, want %s:\n%s", status["state"], want, stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	if code, answer := call(http.MethodPut, "/v1/credentials", map[string]any{
		"cella": map[string]any{"token": cellaToken, "expires_at": exp}, "platform": map[string]any{"token": platformToken, "expires_at": exp},
	}); code != http.StatusOK {
		t.Fatalf("the tokens answered %d %v", code, answer)
	}
	code, proposal := call(http.MethodGet, "/v1/connect?network=none&name=E2E+socket+computer&folder="+url.QueryEscape(project), nil)
	if code != http.StatusOK {
		t.Fatalf("the proposal answered %d %v", code, proposal)
	}
	if code, answer := call(http.MethodPost, "/v1/connect", map[string]any{"policy": proposal["policy"], "consent": proposal["consent"]}); code != http.StatusOK {
		t.Fatalf("the connect answered %d %v", code, answer)
	}
	status := waitState("online")
	id, _ := status["device"].(string)
	if !regexp.MustCompile(`^dev-[a-z2-7]{16}$`).MatchString(id) {
		t.Fatalf("the device is %q", id)
	}
	environment, _, err := cella.GetEnvironment(context.Background(), id)
	if err != nil || environment.Spec.Mode != "worker" {
		t.Fatalf("the environment reads %+v, %v", environment, err)
	}
	if code, answer := call(http.MethodPut, "/v1/policy", map[string]any{"name": "Renamed through the socket"}); code != http.StatusOK {
		t.Fatalf("the rename answered %d %v", code, answer)
	}
	mu.Lock()
	name := named[id]
	seen := append([]string(nil), bearers...)
	mu.Unlock()
	if name != "Renamed through the socket" {
		t.Errorf("the device is named %q at the platform", name)
	}
	for _, b := range seen {
		if b != platformToken {
			t.Errorf("the platform was presented %q, not the handed token", b)
		}
	}

	if code, answer := call(http.MethodPost, "/v1/remove", nil); code != http.StatusOK || answer["state"] != "not_connected" {
		t.Fatalf("the removal answered %d %v", code, answer)
	}
	if _, _, err := cella.GetEnvironment(context.Background(), id); err == nil {
		t.Error("the environment outlived the removal")
	}
	if _, err := os.Stat(filepath.Join(config, "latere", "device.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the host policy file outlived the removal: %v", err)
	}
	select {
	case err := <-exited:
		t.Fatalf("serve ended with the removal: %v", err)
	default:
	}
}
