// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/devices"
)

// connectedForLogout seeds a login on a computer connected as a device, and
// a server that is both the issuer and the platform, recording what each
// was asked in order.
func connectedForLogout(t *testing.T, deleteStatus int) (paths devices.Paths, authPath string, srv *httptest.Server, order func() []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	authPath = seedLogin(t)
	var mu sync.Mutex
	var steps []string
	record := func(step string) {
		mu.Lock()
		steps = append(steps, step)
		mu.Unlock()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		record("revoke")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /v1/account/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		record("remove " + r.PathValue("id"))
		w.WriteHeader(deleteStatus)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("LATERE_PLATFORM_URL", srv.URL)
	t.Setenv("LATERE_PLATFORM_TOKEN", "platform-bearer")
	var err error
	if paths, err = devices.DefaultPaths(); err != nil {
		t.Fatal(err)
	}
	if err = devices.SavePolicy(paths.PolicyFile(), devices.Policy{
		Device: "dev-abcdefghijklmnop", Name: "Test Mac", Folders: []devices.Folder{{Path: "~/project", Write: true}},
		Network: devices.Network{Mode: devices.NetworkNone},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = devices.OpenKeys(paths.KeyFile()).Put(devices.Key{Environment: "dev-abcdefghijklmnop", Token: "k", JTI: "j", MintedAt: now, ExpiresAt: now.Add(devices.KeyTTL)}); err != nil {
		t.Fatal(err)
	}
	return paths, authPath, srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), steps...)
	}
}

func logoutWith(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRoot("test")
	var errBuf bytes.Buffer
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(new(bytes.Buffer))
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"logout"}, args...))
	err := root.Execute()
	return errBuf.String(), err
}

func TestLogoutWarnsOnAConnectedComputer(t *testing.T) {
	t.Run("a no keeps the login and the device", func(t *testing.T) {
		paths, authPath, srv, order := connectedForLogout(t, http.StatusNoContent)
		errOut, err := logoutWith(t, "n\n", "--auth-url", srv.URL)
		if devices.CodeOf(err) != devices.CodeLogoutCancelled {
			t.Fatalf("logout answered no returned %v", err)
		}
		if !strings.Contains(errOut, `connected as your device "Test Mac"`) || !strings.Contains(errOut, "removes it from your devices") ||
			!strings.Contains(errOut, "[y/N]") {
			t.Errorf("logout did not warn and ask:\n%s", errOut)
		}
		if got := order(); len(got) != 0 {
			t.Errorf("a refused logout reached %v", got)
		}
		if _, err := os.Stat(authPath); err != nil {
			t.Errorf("a refused logout removed the login: %v", err)
		}
		if _, ok, _ := devices.OpenKeys(paths.KeyFile()).Get("dev-abcdefghijklmnop"); !ok {
			t.Error("a refused logout deleted the key")
		}
	})
	t.Run("a yes removes the device before the login is revoked", func(t *testing.T) {
		paths, authPath, srv, order := connectedForLogout(t, http.StatusNoContent)
		errOut, err := logoutWith(t, "y\n", "--auth-url", srv.URL)
		if err != nil {
			t.Fatalf("logout returned %v:\n%s", err, errOut)
		}
		if got := strings.Join(order(), ", "); got != "remove dev-abcdefghijklmnop, revoke" {
			t.Errorf("logout asked %q; want the device removed, then the login revoked", got)
		}
		if _, ok, _ := devices.OpenKeys(paths.KeyFile()).Get("dev-abcdefghijklmnop"); ok {
			t.Error("the removed device's key is kept")
		}
		policy, ok, _ := devices.LoadPolicy(paths.PolicyFile())
		if !ok || policy.Device != "" || len(policy.Folders) != 1 {
			t.Errorf("the host policy file reads %+v, %v; want its folders and no device", policy, ok)
		}
		if _, err := os.Stat(authPath); !os.IsNotExist(err) {
			t.Error("the login was kept")
		}
		if !strings.Contains(errOut, "Removed this computer from your devices.") {
			t.Errorf("logout did not say it removed the computer:\n%s", errOut)
		}
	})
	t.Run("--yes does not ask", func(t *testing.T) {
		_, _, srv, order := connectedForLogout(t, http.StatusNotFound)
		errOut, err := logoutWith(t, "", "--auth-url", srv.URL, "--yes")
		if err != nil || strings.Contains(errOut, "[y/N]") {
			t.Fatalf("logout --yes returned %v:\n%s", err, errOut)
		}
		if got := strings.Join(order(), ", "); got != "remove dev-abcdefghijklmnop, revoke" {
			t.Errorf("logout --yes asked %q", got)
		}
	})
	t.Run("a device that cannot be removed keeps the login", func(t *testing.T) {
		_, authPath, srv, order := connectedForLogout(t, http.StatusServiceUnavailable)
		_, err := logoutWith(t, "yes\n", "--auth-url", srv.URL)
		if devices.CodeOf(err) != devices.CodeLogoutBlocked {
			t.Fatalf("logout with the platform down returned %v", err)
		}
		if got := strings.Join(order(), ", "); got != "remove dev-abcdefghijklmnop" {
			t.Errorf("logout asked %q; the login must not be revoked", got)
		}
		if _, err := os.Stat(authPath); err != nil {
			t.Errorf("the login was removed: %v", err)
		}
	})
}
