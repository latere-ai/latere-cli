// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitCredentialSetupMatchesSupportedSchemesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, override, host string
		http                 bool
	}{
		{"production", "", "drive.latere.ai", false},
		{"blank override", " \t ", "drive.latere.ai", false},
		{"development", "localhost:8080", "localhost:8080", true},
		{"padded override", " localhost:8080 ", "localhost:8080", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "gitconfig")
			authPath := filepath.Join(root, "auth-token.json")
			if err := os.WriteFile(authPath, []byte(`{"access_token":"saved-root"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte("[user]\n\tname = Unrelated Setting\n"), 0600); err != nil {
				t.Fatal(err)
			}
			env := append(os.Environ(), "PATH="+filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"), "DRIVE_HOST="+tc.override, "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+authPath, "GIT_CONFIG_GLOBAL="+config, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			run := func(program, input string, args ...string) (string, error) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, program, args...)
				command.Dir, command.Env, command.Stdin = root, env, strings.NewReader(input)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if err != nil {
					t.Logf("%s: %v; %s", filepath.Base(program), err, stderr.String())
				}
				return stdout.String(), err
			}
			for pass := range 2 {
				if _, err := run(binary, "", "git-credential", "setup"); err != nil {
					t.Fatal(err)
				}
				for _, scheme := range []string{"https", "http"} {
					allowed := scheme == "https" || tc.http
					key := "credential." + scheme + "://" + tc.host + ".helper"
					values, err := run(git, "", "config", "--global", "--get-all", key)
					if allowed {
						if err != nil || values != "\n!latere git-credential\n" {
							t.Errorf("setup pass %d, %s entries=%q (%v)", pass, scheme, values, err)
						}
					} else if values != "" {
						t.Errorf("production HTTP helper configured: %q", values)
					}
					input := "protocol=" + scheme + "\nhost=" + tc.host + "\n\n"
					out, err := run(git, input, "credential", "fill")
					if allowed {
						if err != nil || !strings.Contains(out, "password=saved-root\n") {
							t.Errorf("Git did not obtain %s credential: %v, %q", scheme, err, out)
						}
					} else if err == nil || out != "" {
						t.Errorf("Git unexpectedly obtained HTTP credential: %v, %q", err, out)
					}
				}
			}
			for range 2 {
				if _, err := run(binary, "", "git-credential", "setup", "--remove"); err != nil {
					t.Fatal(err)
				}
			}
			for _, scheme := range []string{"https", "http"} {
				if values, _ := run(git, "", "config", "--global", "--get-all", "credential."+scheme+"://"+tc.host+".helper"); values != "" {
					t.Errorf("removed setup retained %s helpers: %q", scheme, values)
				}
			}
			if value, err := run(git, "", "config", "--global", "--get", "user.name"); err != nil || value != "Unrelated Setting\n" {
				t.Errorf("setup modified unrelated config: %q (%v)", value, err)
			}
		})
	}
}
