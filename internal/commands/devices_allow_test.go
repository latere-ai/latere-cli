// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"latere.ai/x/cella/device"
)

// controlStub answers the engine's host routes on this computer's control
// socket, recording each request, with the answer the test sets.
type controlStub struct {
	mu       sync.Mutex
	requests []string
	status   int
	answer   string
}

func (c *controlStub) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requests)
}

func (c *controlStub) answerWith(status int, answer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status, c.answer = status, answer
}

func listenControlStub(t *testing.T, paths device.Paths) *controlStub {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(paths.Socket()), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.Socket())
	if err != nil {
		t.Fatal(err)
	}
	c := &controlStub{status: http.StatusNoContent}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.requests = append(c.requests, r.Method+" "+r.URL.Path+" "+string(body))
		status, answer := c.status, c.answer
		c.mu.Unlock()
		if answer != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return c
}

func TestDevicesAllowThroughTheEngine(t *testing.T) {
	paths, project := deviceHome(t)
	connectedHere(t, paths, project)
	stub := listenControlStub(t, paths)

	_, errOut, err := runDevices(t, "", "devices", "allow", "api.example.com", "--session", "ses_01jaq7v3k9m2wxyz")
	if err != nil {
		t.Fatalf("allow --session returned %v", err)
	}
	if !strings.Contains(errOut, "Allowed api.example.com for ses_01jaq7v3k9m2wxyz: its next command reaches it") {
		t.Errorf("allow --session said %q", errOut)
	}
	_, errOut, err = runDevices(t, "", "devices", "allow", "db.example.com:5432", "--save")
	if err != nil || !strings.Contains(errOut, "Saved db.example.com:5432 to the host policy file: every session's next command reaches it.") {
		t.Fatalf("allow --save returned %v: %q", err, errOut)
	}
	// An address with a port is written in brackets.
	if _, _, err = runDevices(t, "", "devices", "allow", "[2001:db8::1]:8443", "--session", "sbx_1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /v1/sessions/ses_01jaq7v3k9m2wxyz/hosts {"host":"api.example.com","port":443}`,
		`POST /v1/policy/hosts {"host":"db.example.com","port":5432}`,
		`POST /v1/sessions/sbx_1/hosts {"host":"2001:db8::1","port":8443}`,
	}
	if got := stub.seen(); !slices.Equal(got, want) {
		t.Errorf("the engine was asked\n%v\nwant\n%v", got, want)
	}
	// Under the network any the file the engine saved to says so, and an
	// older latere would refuse it.
	withNetwork(t, paths, device.Network{Mode: device.NetworkAny})
	if _, errOut, err = runDevices(t, "", "devices", "allow", "api.example.com", "--save"); err != nil || !strings.Contains(errOut, "`latere upgrade`") {
		t.Errorf("allow --save under any returned %v: %q", err, errOut)
	}

	// The engine's refusals reach the person with their own sentences.
	refusal := func(code, message, detail string) string {
		raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{"detail": detail}}})
		return string(raw)
	}
	for _, tc := range []struct {
		status            int
		answer, code, say string
	}{
		{http.StatusNotFound, refusal("no_session", "No sandbox of this session runs on this computer.", "no sandbox of this session runs on this computer"),
			codeNoSession, "nothing was allowed"},
		{http.StatusUnprocessableEntity, refusal(device.CodeHostNotAllowed, "This host cannot be allowed here.", "api.example.com is blocked by *.example.com"),
			device.CodeHostNotAllowed, "blocked by *.example.com"},
		{http.StatusConflict, refusal("not_serving", "This computer is not serving right now.", ""), device.CodeNotServing, "latere devices connect"},
		{http.StatusTeapot, refusal("brewing", "Something else.", ""), device.CodeControlUnavailable, "brewing"},
		{http.StatusNotFound, "404 page not found", device.CodeControlUnavailable, "404"},
	} {
		stub.answerWith(tc.status, tc.answer)
		if _, _, err = runDevices(t, "", "devices", "allow", "api.example.com", "--session", "ses_1"); device.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.say) {
			t.Errorf("an answer of %d %s returned %v", tc.status, tc.answer, err)
		}
	}

	// What cannot be a host, a port or a session is refused here, and the
	// engine is never asked.
	stub.answerWith(http.StatusNoContent, "")
	before := len(stub.seen())
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"bad host", "--save"}, device.CodeHostNotAllowed},
		{[]string{"example.com:http", "--save"}, device.CodeHostNotAllowed},
		{[]string{"example.com:70000", "--save"}, device.CodeHostNotAllowed},
		{[]string{"example.com", "--session", " "}, codeNoSession},
	} {
		if _, _, err = runDevices(t, "", append([]string{"devices", "allow"}, tc.args...)...); device.CodeOf(err) != tc.code {
			t.Errorf("allow %v returned %v", tc.args, err)
		}
	}
	for _, args := range [][]string{{"example.com"}, {"example.com", "--save", "--session", "ses_1"}, {"--save"}} {
		if _, _, err = runDevices(t, "", append([]string{"devices", "allow"}, args...)...); err == nil {
			t.Errorf("allow %v was taken", args)
		}
	}
	if got := stub.seen(); len(got) != before {
		t.Errorf("a refused allow reached the engine: %v", got[before:])
	}
}

func TestDevicesAllowWithNothingServing(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	acceptAny(t)
	connectedHere(t, paths, project)

	// A session's allowance lives in the engine that serves it.
	if _, _, err := runDevices(t, "", "devices", "allow", "api.example.com", "--session", "ses_1"); device.CodeOf(err) != device.CodeNotServing {
		t.Errorf("allow --session with nothing serving returned %v", err)
	}
	// The network none reaches no host, so nothing is saved.
	if _, _, err := runDevices(t, "", "devices", "allow", "api.example.com", "--save"); device.CodeOf(err) != device.CodeHostNotAllowed {
		t.Errorf("allow --save under none returned %v", err)
	}

	// Saved, the host joins the allowlist, read at the next connect.
	withNetwork(t, paths, device.Network{Mode: device.NetworkAllowlist, Hosts: []string{"github.com"}})
	_, errOut, err := runDevices(t, "", "devices", "allow", "api.example.com", "--save")
	if err != nil || !strings.Contains(errOut, "Saved api.example.com to the host policy file: sessions reach it from the next `latere devices connect`.") {
		t.Fatalf("allow --save with nothing serving returned %v: %q", err, errOut)
	}
	if _, _, err = runDevices(t, "", "devices", "allow", "db.example.com:5432", "--save"); err != nil {
		t.Fatal(err)
	}
	if n := policyNetwork(t, paths); !slices.Equal(n.Hosts, []string{"github.com", "api.example.com", "db.example.com:5432"}) {
		t.Errorf("the allowlist after two saves is %+v", n)
	}

	// Under any, saving a host lifts its own block, and a *. block stays.
	withNetwork(t, paths, device.Network{Mode: device.NetworkAny, Blocked: []string{"api.example.com", "*.internal.example.com"}})
	if _, errOut, err = runDevices(t, "", "devices", "allow", "api.example.com", "--save"); err != nil || !strings.Contains(errOut, "`latere upgrade`") {
		t.Fatalf("allow --save of a blocked host returned %v: %q", err, errOut)
	}
	if n := policyNetwork(t, paths); n.Mode != device.NetworkAny || !slices.Equal(n.Blocked, []string{"*.internal.example.com"}) {
		t.Errorf("the block list after the save is %+v", n)
	}
	if _, _, err = runDevices(t, "", "devices", "allow", "db.internal.example.com", "--save"); device.CodeOf(err) != device.CodeHostNotAllowed ||
		!strings.Contains(err.Error(), "*.internal.example.com") {
		t.Errorf("allow --save of a host a wildcard blocks returned %v", err)
	}
}

// The activity log holds each refused host and each one allowed or saved,
// with its session and port.
func TestDevicesActivityShowsRefusedHosts(t *testing.T) {
	paths, project := deviceHome(t)
	id := connectedHere(t, paths, project)
	log, err := device.OpenActivity(paths.ActivityDir(id))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, e := range []device.Entry{
		{Time: now, Type: device.TypeNetworkRefused, Session: "ses_r", Agent: "agt_1", Sandbox: "sbx_r", Host: "api.example.com", Port: 443},
		{Time: now, Type: device.TypeNetworkRefused, Sandbox: "sbx_n", Host: "2001:db8::1", Port: 8443},
		{Time: now, Type: device.TypeHostAllowed, Session: "ses_r", Sandbox: "sbx_r", Host: "api.example.com", Port: 443},
		{Time: now, Type: device.TypeHostSaved, Host: "db.example.com", Port: 5432},
	} {
		if err = log.Write(e); err != nil {
			t.Fatal(err)
		}
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	out, _, err := runDevices(t, "", "devices", "activity")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ses_r  network_refused  api.example.com:443", "sbx_n  network_refused  [2001:db8::1]:8443",
		"ses_r  host_allowed  api.example.com:443", "-  host_saved  db.example.com:5432",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the activity lacks %q:\n%s", want, out)
		}
	}
	out, _, err = runDevices(t, "", "devices", "activity", "--json")
	if err != nil || !strings.Contains(out, `"type":"network_refused"`) || !strings.Contains(out, `"host":"api.example.com","port":443`) {
		t.Errorf("the JSON activity is %s, %v", out, err)
	}
}
