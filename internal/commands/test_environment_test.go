// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestMain(m *testing.M) {
	os.Exit(runIsolatedCommandTests(m))
}

// Establish safe defaults before any command test runs. Individual tests can
// still override these with t.Setenv, but forgetting to do so never falls back
// to the caller's saved login or an explicit token-file environment override.
//
// root is also the temporary directory for this process and everything it
// spawns. Tests here start child processes they later kill, and a killed
// process runs no deferred cleanup, so the directory it created can only be
// reaped by an ancestor that outlives it. Nesting every descendant's temporary
// state under the one directory this function removes makes that automatic.
func runIsolatedCommandTests(m *testing.M) int {
	root, err := os.MkdirTemp("", "latere-command-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(root)
	for key, value := range map[string]string{
		"XDG_CONFIG_HOME":        root,
		"LATERE_TOKEN_FILE":      filepath.Join(root, "latere", "token.json"),
		"LATERE_AUTH_TOKEN_FILE": filepath.Join(root, "latere", "auth-token.json"),
		// os.TempDir reads TMPDIR on unix, and TMP then TEMP on Windows.
		"TMPDIR": root,
		"TMP":    root,
		"TEMP":   root,
	} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}

func TestCommandSuiteIsolatesSavedLogin(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		name := "default paths"
		if explicit {
			name = "explicit paths"
		}
		t.Run(name, func(t *testing.T) {
			callerConfig := t.TempDir()
			credentials := filepath.Join(callerConfig, "latere")
			if explicit {
				credentials = t.TempDir()
			}
			if err := os.MkdirAll(credentials, 0700); err != nil {
				t.Fatal(err)
			}
			tokenPath, authPath := filepath.Join(credentials, "token.json"), filepath.Join(credentials, "auth-token.json")
			before := `{"access_token":"synthetic-caller-token","refresh_token":"synthetic-refresh"}`
			for _, path := range []string{tokenPath, authPath} {
				if err := os.WriteFile(path, []byte(before), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run=^TestCommandSuiteLoginIsolationHelper$", "-test.count=1")
			child.Env = append(os.Environ(), "LATERE_TEST_LOGIN_ISOLATION_HELPER=1", "XDG_CONFIG_HOME="+callerConfig, "LATERE_TOKEN_FILE=", "LATERE_AUTH_TOKEN_FILE=")
			if explicit {
				child.Env = append(child.Env, "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+authPath)
			}
			if out, err := child.CombinedOutput(); err != nil {
				t.Errorf("command test subprocess: %v\n%s", err, out)
			}
			for _, path := range []string{tokenPath, authPath} {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != before {
					t.Errorf("command tests modified the caller's %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

// This subprocess intentionally omits per-test path overrides, just as a new
// command test might. Its caller supplies synthetic credentials, so a failing
// isolation regression cannot touch the developer's actual login.
func TestCommandSuiteLoginIsolationHelper(t *testing.T) {
	if os.Getenv("LATERE_TEST_LOGIN_ISOLATION_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	if _, err := api.LoadToken(""); !errors.Is(err, api.ErrNoToken) {
		t.Errorf("test suite inherited Cella credentials: %v", err)
	}
	if _, err := api.LoadAuthToken(); !errors.Is(err, api.ErrNoToken) {
		t.Errorf("test suite inherited auth credentials: %v", err)
	}
	if err := api.SaveToken("", api.Token{AccessToken: "synthetic-test-token"}); err != nil {
		t.Fatal(err)
	}
	if err := api.SaveAuthToken(api.Token{AccessToken: "synthetic-test-auth"}); err != nil {
		t.Fatal(err)
	}
	if err := api.ClearToken(""); err != nil {
		t.Fatal(err)
	}
	if err := api.ClearAuthToken(); err != nil {
		t.Fatal(err)
	}
}

// A child process this suite kills cannot run its own cleanup, so the
// temporary directory it created survives until an ancestor removes it. The
// invariant is therefore not that a killed descendant creates nothing, but
// that whatever it leaves nests under the root runIsolatedCommandTests
// removes. Assert it end to end: run the cancellation tests in a nested test
// binary pointed at a temporary directory this test owns, then require that
// directory to be empty once the nested binary has exited.
func TestCommandSuiteReapsTempDirsOfKilledChildren(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	outer := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	nested := exec.CommandContext(ctx, binary, "-test.run=^TestHostSandboxReportsCanceledCommands$", "-test.count=1")
	nested.Env = append(os.Environ(), "TMPDIR="+outer, "TMP="+outer, "TEMP="+outer)
	if out, err := nested.CombinedOutput(); err != nil {
		t.Fatalf("nested cancellation run: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("killed children left %d entries under TMPDIR: %v", len(entries), names)
	}
}
