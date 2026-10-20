// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"latere.ai/x/cella/device"
	"latere.ai/x/pkg/httpjson"
)

// connect proposes session_start from --session-start, then from an earlier
// file, then allow.
func TestConnectProposesSessionStart(t *testing.T) {
	home := t.TempDir()
	base := connectRequest{folders: []string{home + "/project"}, network: device.NetworkNone, sessionStart: device.SessionStartAllow}
	for _, tc := range []struct {
		name     string
		previous string
		flag     string
		want     string
	}{
		{"the default", "", "", device.SessionStartAllow},
		{"the flag", "", device.SessionStartAsk, device.SessionStartAsk},
		{"an earlier file", device.SessionStartAsk, "", device.SessionStartAsk},
		{"the flag over an earlier file", device.SessionStartAsk, device.SessionStartAllow, device.SessionStartAllow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			if tc.flag != "" {
				r.sessionStart, r.sessionStartSet = tc.flag, true
			}
			p, err := r.proposal(device.Policy{SessionStart: tc.previous}, home)
			if err != nil || p.SessionStart != tc.want {
				t.Errorf("proposal = %q, %v; want %q", p.SessionStart, err, tc.want)
			}
		})
	}
}

func TestConnectSessionStartFlag(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	// A value that is neither is refused before anything is shown or made.
	_, errOut, err := runDevices(t, "yes\n", "devices", "connect", "--folder", project, "--session-start", "sometimes")
	if device.CodeOf(err) != device.CodePolicyInvalid || !strings.Contains(err.Error(), "session_start") || strings.Contains(errOut, "Type yes") {
		t.Errorf("--session-start sometimes returned %v:\n%s", err, errOut)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("--session-start sometimes reached the platform: %v", got)
	}
	// ask is shown on the screen before the typed yes.
	_, errOut, err = runDevices(t, "no\n", "devices", "connect", "--folder", project, "--session-start", "ask")
	if device.CodeOf(err) != device.CodeNotConfirmed || !strings.Contains(errOut, "Each session asks for your approval here before it starts.") {
		t.Errorf("--session-start ask showed:\n%s\nand returned %v", errOut, err)
	}
	// On a connected computer the flag is refused like the others: the
	// host policy file, or `latere devices policy --session-start`, is
	// where it changes.
	connectedHere(t, paths, project)
	if _, _, err = runDevices(t, "", "devices", "connect", "--session-start", "ask"); device.CodeOf(err) != device.CodeAlreadyConnected ||
		!strings.Contains(device.Fail(device.CodeAlreadyConnected, "", nil).Hint, "latere devices policy --session-start") {
		t.Errorf("--session-start on a connected computer returned %v", err)
	}
}

// latere devices pause and resume ask the engine serving this computer
// through its control socket, and say so when nothing serves.
func TestDevicesPauseAndResume(t *testing.T) {
	paths, _ := deviceHome(t)
	for _, verb := range []string{"pause", "resume"} {
		if _, errOut, err := runDevices(t, "", "devices", verb); device.CodeOf(err) != device.CodeNotServing || errOut != "" {
			t.Errorf("%s with nothing serving returned %v, %q", verb, err, errOut)
		}
	}
	if err := os.MkdirAll(filepath.Dir(paths.Socket()), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.Socket())
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		asked []string
	)
	mux := http.NewServeMux()
	for _, route := range []string{"/v1/pause", "/v1/resume"} {
		mux.HandleFunc("POST "+route, func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			asked = append(asked, route)
			mu.Unlock()
			httpjson.Write(w, http.StatusOK, device.Status{State: device.StatePaused})
		})
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, errOut, err := runDevices(t, "", "devices", "pause")
	if err != nil || !strings.Contains(errOut, "Paused. Every command a session started on this computer was stopped") {
		t.Errorf("pause returned %v, %q", err, errOut)
	}
	_, errOut, err = runDevices(t, "", "devices", "resume")
	if err != nil || !strings.Contains(errOut, "Resumed.") {
		t.Errorf("resume returned %v, %q", err, errOut)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "/v1/pause,/v1/resume" {
		t.Errorf("the socket was asked %v", asked)
	}
}
