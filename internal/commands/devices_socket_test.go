// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"latere.ai/x/cella/device"
	"latere.ai/x/sdk-go/latere"
)

// `latere devices serve` driven through its control socket alone, as a
// desktop client drives it (spec 018).

// lockedBuffer is a buffer a running command writes while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// served is a running `latere devices serve` and a client of its socket.
type served struct {
	client *http.Client
	stderr *lockedBuffer
	exited chan error
}

// serving runs `latere devices serve` with args until the test ends, and
// waits for its control socket to answer.
func serving(t *testing.T, paths device.Paths, args ...string) *served {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	root := NewRoot("test")
	out, errOut := &lockedBuffer{}, &lockedBuffer{}
	root.SetIn(strings.NewReader(""))
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(append([]string{"devices", "serve"}, args...))
	s := &served{stderr: errOut, exited: make(chan error, 1)}
	go func() { s.exited <- root.ExecuteContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.exited:
		case <-time.After(30 * time.Second):
			t.Error("serve did not stop inside the deadline")
		}
	})
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket())
	}}}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if res, err := s.client.Get("http://device/v1/status"); err == nil {
			_ = res.Body.Close()
			return s
		}
		select {
		case err := <-s.exited:
			t.Fatalf("serve ended with %v before its socket answered:\n%s", err, errOut.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the control socket never answered")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// call is a request to the local API with a JSON body, answering the status
// code and the decoded answer.
func (s *served) call(t *testing.T, method, route string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://device"+route, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var answer map[string]any
	_ = json.NewDecoder(res.Body).Decode(&answer)
	return res.StatusCode, answer
}

// waitState polls the status until its state is state.
func (s *served) waitState(t *testing.T, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, status := s.call(t, http.MethodGet, "/v1/status", nil)
		if status["state"] == state {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("the state stayed %v, want %s:\n%s", status["state"], state, s.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// refusalCode is the code of a refusal of the local API.
func refusalCode(answer map[string]any) string {
	e, _ := answer["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// unreadableLogin puts a saved login no process may read, so a command that
// tried to read it would fail.
func unreadableLogin(t *testing.T) {
	t.Helper()
	path := os.Getenv("LATERE_AUTH_TOKEN_FILE")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"access_token":"SAVED-LOGIN"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this user reads a file of mode 0000, so a read of the saved login would go unnoticed")
	}
}

func TestServeWaitsNotConnectedCommand(t *testing.T) {
	paths, _ := deviceHome(t)
	sandboxStandIn(t)
	p := newDevicePlatform(t)
	s := serving(t, paths)
	status := s.waitState(t, device.StateNotConnected)
	if status["device"] != "" || status["online"] != false {
		t.Errorf("a computer not connected reads %v", status)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("waiting reached the platform: %v", got)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(s.stderr.String(), "Not connected.") {
		if time.Now().After(deadline) {
			t.Fatalf("serve did not say it waits:\n%s", s.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServeRefusesASecondEngine(t *testing.T) {
	paths, _ := deviceHome(t)
	newDevicePlatform(t)
	lock, err := device.TakeLock(paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if _, _, err := runDevices(t, "", "devices", "serve", "--credentials", "client"); device.CodeOf(err) != device.CodeAlreadyServed {
		t.Errorf("a second serve returned %v", err)
	}
}

func TestServeRefusesAnUnknownCredentialsSource(t *testing.T) {
	deviceHome(t)
	if _, _, err := runDevices(t, "", "devices", "serve", "--credentials", "keychain"); err == nil || !strings.Contains(err.Error(), "login or client") {
		t.Errorf("an unknown --credentials returned %v", err)
	}
}

func TestConnectThroughSocketWithClientCredentials(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	p := newDevicePlatform(t)
	// The worker keeps registering, so the computer stays served.
	p.register = func(w http.ResponseWriter) {
		http.Error(w, "the stub keeps the worker waiting", http.StatusServiceUnavailable)
	}
	unreadableLogin(t)
	s := serving(t, paths, "--credentials", "client")
	status := s.waitState(t, device.StateNotConnected)
	if creds, _ := status["credentials"].(map[string]any); creds["cella"] != device.CredentialNeeded || creds["platform"] != device.CredentialNeeded {
		t.Errorf("a client's engine with no tokens reads %v", status["credentials"])
	}

	exp := time.Now().Add(time.Hour)
	cellaToken := fakeJWT(t, map[string]any{"sub": "p1", "aud": device.AudienceCella, "exp": exp.Unix()})
	platformToken := fakeJWT(t, map[string]any{"sub": "p1", "aud": latere.DefaultAudience, "exp": exp.Unix()})
	code, answer := s.call(t, http.MethodPut, "/v1/credentials", map[string]any{
		"cella": map[string]any{"token": cellaToken, "expires_at": exp}, "platform": map[string]any{"token": platformToken, "expires_at": exp},
	})
	if creds, _ := answer["credentials"].(map[string]any); code != http.StatusOK || creds["cella"] != device.CredentialValid || creds["platform"] != device.CredentialValid {
		t.Fatalf("the tokens answered %d %v", code, answer)
	}

	code, proposal := s.call(t, http.MethodGet, "/v1/connect?name=Test+Mac&host=github.com&folder="+url.QueryEscape(project), nil)
	if code != http.StatusOK {
		t.Fatalf("the proposal answered %d %v", code, proposal)
	}
	if problems, _ := proposal["problems"].([]any); len(problems) != 0 {
		t.Fatalf("the proposal has problems: %v", problems)
	}
	stale := map[string]any{"policy": proposal["policy"], "consent": "sha256:0000"}
	if code, answer := s.call(t, http.MethodPost, "/v1/connect", stale); code != http.StatusUnprocessableEntity || refusalCode(answer) != device.CodeNotConfirmed {
		t.Errorf("a stale consent answered %d %v", code, answer)
	}
	if code, answer := s.call(t, http.MethodPost, "/v1/connect", map[string]any{"policy": proposal["policy"], "consent": proposal["consent"]}); code != http.StatusOK {
		t.Fatalf("the connect answered %d %v\n%s", code, answer, s.stderr.String())
	}
	policy, _, err := device.LoadPolicy(paths.PolicyFile())
	if err != nil || !strings.HasPrefix(policy.Device, "dev-") || policy.Name != "Test Mac" || policy.Folders[0].Path != "~/project" ||
		strings.Join(policy.Network.Hosts, ",") != "github.com" {
		t.Fatalf("the host policy file reads %+v, %v", policy, err)
	}
	id := policy.Device
	p.mu.Lock()
	renamed := p.renamed[id]
	p.mu.Unlock()
	if renamed != "Test Mac" {
		t.Errorf("the device was named %q", renamed)
	}
	// The worker registers once the key is kept, so the keychain is read
	// after the engine wrote it.
	deadline := time.Now().Add(15 * time.Second)
	for !slices.ContainsFunc(p.seen(), func(r string) bool { return strings.HasSuffix(r, "/workers") }) {
		if time.Now().After(deadline) {
			t.Fatalf("the worker never registered:\n%s", s.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if key, ok, err := paths.Keys().Get(id); !ok || err != nil || key.Token != "ENVIRONMENT-KEY-SECRET" {
		t.Errorf("the keychain holds %+v, %v, %v", key, ok, err)
	}

	// Every request presented the token handed for its audience, never the
	// saved login, which no step read.
	p.mu.Lock()
	bearers := append([]string(nil), p.bearers...)
	p.mu.Unlock()
	for _, b := range bearers {
		switch {
		case strings.Contains(b, "/workers"):
		case strings.Contains(b, " /v1/environments/") && !strings.HasSuffix(b, " "+cellaToken):
			t.Errorf("a Cella request presented another bearer: %s", b)
		case strings.Contains(b, " /v1/account/") && !strings.HasSuffix(b, " "+platformToken):
			t.Errorf("a platform request presented another bearer: %s", b)
		}
	}
	for _, f := range []string{paths.PolicyFile(), filepath.Join(paths.ActivityDir(id), "activity.ndjson")} {
		if raw, err := os.ReadFile(f); err == nil && (bytes.Contains(raw, []byte(cellaToken)) || bytes.Contains(raw, []byte(platformToken))) {
			t.Errorf("%s holds a handed token", f)
		}
	}
	if strings.Contains(s.stderr.String(), cellaToken) || strings.Contains(s.stderr.String(), platformToken) {
		t.Error("a handed token was printed")
	}

	t.Run("the name changes through the socket and at the platform", func(t *testing.T) {
		code, answer := s.call(t, http.MethodPut, "/v1/policy", map[string]any{"name": "Work Mac"})
		if code != http.StatusOK || answer["name"] != "Work Mac" {
			t.Fatalf("the rename answered %d %v", code, answer)
		}
		p.mu.Lock()
		renamed := p.renamed[id]
		p.mu.Unlock()
		if renamed != "Work Mac" {
			t.Errorf("the platform was told the name %q", renamed)
		}
	})

	t.Run("the removal reaches the platform and forgets the device here", func(t *testing.T) {
		code, answer := s.call(t, http.MethodPost, "/v1/remove", nil)
		if code != http.StatusOK || answer["state"] != device.StateNotConnected {
			t.Fatalf("the removal answered %d %v", code, answer)
		}
		p.mu.Lock()
		removed := append([]string(nil), p.removed...)
		p.mu.Unlock()
		if len(removed) != 1 || removed[0] != id {
			t.Errorf("the platform removed %v", removed)
		}
		if _, ok, _ := paths.Keys().Get(id); ok {
			t.Error("the key is still kept")
		}
		if _, err := os.Stat(paths.PolicyFile()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the host policy file is still there: %v", err)
		}
		select {
		case err := <-s.exited:
			t.Fatalf("serve ended with the removal: %v", err)
		default:
		}
	})
}
