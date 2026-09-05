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
