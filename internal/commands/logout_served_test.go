// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"latere.ai/x/cella/device"
)

// envHoldLock names the lock the helper process below holds.
const envHoldLock = "LATERE_TEST_HOLD_ENGINE_LOCK"

// holdLockInAnotherProcess takes the engine's lock at path in a child
// process, as Latere Studio holds it while it serves this computer, and
// keeps it until the test ends.
func holdLockInAnotherProcess(t *testing.T, path string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	child := exec.CommandContext(ctx, binary, "-test.run=^TestHoldTheEngineLockHelper$", "-test.count=1")
	child.Env = append(os.Environ(), envHoldLock+"="+path)
	stdin, err := child.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	out := bufio.NewReader(stdout)
	t.Cleanup(func() {
		defer cancel()
		if err := stdin.Close(); err != nil {
			t.Errorf("close the helper's stdin: %v", err)
		}
		rest, err := io.ReadAll(out)
		if err != nil {
			t.Errorf("read the helper's output: %v", err)
		}
		if err := child.Wait(); err != nil {
			t.Errorf("the process holding the lock ended with %v:\n%s", err, rest)
		}
	})
	line, err := out.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "held" {
		t.Fatalf("the helper did not take the lock (%v): %q", err, line)
	}
}

// TestHoldTheEngineLockHelper is the child process of
// holdLockInAnotherProcess: it takes the lock, says so, and holds it until
// its stdin closes.
func TestHoldTheEngineLockHelper(t *testing.T) {
	path := os.Getenv(envHoldLock)
	if path == "" {
		t.Skip("subprocess helper")
	}
	lock, err := device.TakeLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Fprintln(os.Stdout, "held"); err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, os.Stdin); err != nil {
		t.Error(err)
	}
	if err = lock.Release(); err != nil {
		t.Fatal(err)
	}
}

// While another process serves this computer, such as Latere Studio, logout
// signs out and keeps the device, which is that process's too, without
// asking, also under --yes. With the lock free, it removes the device as
// before and leaves the lock free.
func TestLogoutKeepsAnApplicationsDevice(t *testing.T) {
	for name, args := range map[string][]string{
		"the lock held by another process":             nil,
		"the lock held by another process, with --yes": {"--yes"},
	} {
		t.Run(name, func(t *testing.T) {
			paths, authPath, srv, order := connectedForLogout(t, http.StatusNoContent)
			holdLockInAnotherProcess(t, paths.LockFile())
			errOut, err := logoutWith(t, "", append([]string{"--auth-url", srv.URL}, args...)...)
			if err != nil {
				t.Fatalf("logout returned %v:\n%s", err, errOut)
			}
			if got := strings.Join(order(), ", "); got != "revoke" {
				t.Errorf("logout asked %q; want the login revoked and the device kept", got)
			}
			for _, want := range []string{
				"Latere Studio or `latere devices` in another terminal serves this computer, so it stays connected as\n" +
					`your device "Test Mac" (dev-abcdefghijklmnop); to remove it, run ` + "`latere devices remove` while you are signed in.\n",
				"Logged out.",
			} {
				if !strings.Contains(errOut, want) {
					t.Errorf("logout does not say %q:\n%s", want, errOut)
				}
			}
			if strings.Contains(errOut, "[y/N]") || strings.Contains(errOut, "removes it from your devices") {
				t.Errorf("logout asked to remove a device another process serves:\n%s", errOut)
			}
			if _, ok, _ := device.OpenKeys(paths.KeyFile()).Get("dev-abcdefghijklmnop"); !ok {
				t.Error("logout deleted the key of a device another process serves")
			}
			if policy, _, _ := device.LoadPolicy(paths.PolicyFile()); policy.Device != "dev-abcdefghijklmnop" {
				t.Errorf("the host policy file names %q; want the device kept", policy.Device)
			}
			if _, err := os.Stat(authPath); !os.IsNotExist(err) {
				t.Error("the login was kept")
			}
		})
	}
	t.Run("the lock free", func(t *testing.T) {
		paths, _, srv, order := connectedForLogout(t, http.StatusNoContent)
		errOut, err := logoutWith(t, "", "--auth-url", srv.URL, "--yes")
		if err != nil {
			t.Fatalf("logout returned %v:\n%s", err, errOut)
		}
		if got := strings.Join(order(), ", "); got != "remove dev-abcdefghijklmnop, revoke" {
			t.Errorf("logout asked %q; want the device removed, then the login revoked", got)
		}
		if strings.Contains(errOut, "stays connected") {
			t.Errorf("logout kept a device nothing serves:\n%s", errOut)
		}
		lock, err := device.TakeLock(paths.LockFile())
		if err != nil {
			t.Fatalf("logout left the lock held: %v", err)
		}
		if err = lock.Release(); err != nil {
			t.Fatal(err)
		}
	})
}
