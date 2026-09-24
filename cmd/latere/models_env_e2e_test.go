// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// modelsEnv is the environment of a models e2e run: the key handed in
// through LATERE_MODEL_KEY, so the binary neither needs a login nor touches
// the system keychain, and no saved login to fall back on.
func modelsEnv(root, key string) []string {
	return append(os.Environ(), "LATERE_MODEL_KEY="+key, "LATERE_MODELS_URL=", "AUTH_URL=",
		"LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"),
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
}

func TestModelsEnvExportsLiteralShellValuesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	for _, tc := range []struct {
		name, key, url string
		raw            bool
	}{
		{name: "ordinary", key: "pat_abc.DEF-123_=="},
		{name: "spaces", key: "key with spaces"},
		{name: "quotes", key: "key'with\"quotes"},
		{name: "newline", key: "first\nsecond"},
		{name: "substitution", key: `key$(printf injected > "$MARKER")end`},
		{name: "parameter expansion", key: "key${SHELL_VALUE}end"},
		{name: "URL characters", key: "key", url: "https://models.example/${SHELL_VALUE}?a=1&b=2"},
		{name: "raw stays literal", key: `key$(printf injected > "$MARKER")end`, raw: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			base := tc.url
			if base == "" {
				base = "https://models.example/v1/models"
			}
			args := []string{"models", "env", "--models-url", base}
			if tc.raw {
				args = append(args, "--raw")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = modelsEnv(root, tc.key)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("models env: %v; %s", err, stderr.String())
			}
			if tc.raw {
				if stdout.String() != tc.key+"\n" {
					t.Errorf("raw value changed: %q", stdout.String())
				}
				return
			}
			for _, name := range []string{"sh", "bash", "zsh"} {
				t.Run(name, func(t *testing.T) {
					shell, err := exec.LookPath(name)
					if err != nil {
						t.Skipf("%s unavailable", name)
					}
					marker := filepath.Join(t.TempDir(), "unexpected-command")
					// The only possible command substitution in these synthetic
					// fixtures writes a marker inside this test's temporary directory.
					script := `set -e
 eval "$1"
 printf '%s\000%s' "$OPENAI_BASE_URL" "$OPENAI_API_KEY"`
					shellArgs := []string{"-c", script, "verify-exports", stdout.String()}
					if name == "zsh" {
						shellArgs = append([]string{"-f"}, shellArgs...)
					}
					check := exec.CommandContext(ctx, shell, shellArgs...)
					check.Env = append(os.Environ(), "BASH_ENV=", "ENV=", "ZDOTDIR="+root, "MARKER="+marker, "SHELL_VALUE=expanded", "OPENAI_BASE_URL=", "OPENAI_API_KEY=")
					var got, diagnostic bytes.Buffer
					check.Stdout, check.Stderr = &got, &diagnostic
					err = check.Run()
					want := base + "/openai/v1\x00" + tc.key
					if err != nil || got.String() != want {
						t.Errorf("shell values changed: %v; got=%q want=%q stderr=%q", err, got.String(), want, diagnostic.String())
					}
					if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("shell evaluated data as a command: %v", err)
					}
				})
			}
		})
	}
}

func TestModelsEnvReportsOutputFailuresE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	for _, format := range []string{"exports", "raw"} {
		for _, mode := range []string{"writable", "failed stdout", "failed stderr"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, "output")
				const before = "existing output\n"
				if err := os.WriteFile(dest, []byte(before), 0600); err != nil {
					t.Fatal(err)
				}
				flags := os.O_RDONLY
				if mode == "writable" {
					flags = os.O_WRONLY | os.O_APPEND
				}
				file, err := os.OpenFile(dest, flags, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				args := []string{"models", "env", "--models-url", "https://models.example/v1/models"}
				if format == "raw" {
					args = append(args, "--raw")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = modelsEnv(root, "synthetic-key")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = file, &stderr
				if mode == "failed stderr" {
					command.Stdout, command.Stderr = &stdout, file
				}
				err = command.Run()
				// Both forms write the key's provenance to stderr, so a failed
				// stderr is a failure for both.
				if mode != "writable" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("output failure reported success: %v: %s", err, stderr.String())
					}
					if mode == "failed stdout" && !strings.Contains(stderr.String(), "write") {
						t.Errorf("missing write error: %s", stderr.String())
					}
				} else if err != nil {
					t.Errorf("valid output failed: %v: %s", err, stderr.String())
				}
				want := "synthetic-key\n"
				if format == "exports" {
					want = "export OPENAI_BASE_URL=https://models.example/v1/models/openai/v1\nexport OPENAI_API_KEY=synthetic-key\n"
				}
				wantFile := before
				if mode == "writable" {
					wantFile += want
				}
				if data, err := os.ReadFile(dest); err != nil || string(data) != wantFile {
					t.Errorf("output file=%q (%v), want %q", data, err, wantFile)
				}
				if mode == "failed stderr" && stdout.String() != want {
					t.Errorf("stdout=%q, want %q", stdout.String(), want)
				}
			})
		}
	}
}

// TestModelsNeedALoginOrAKeyE2E: with no handed key, a missing or empty
// saved login stops every models command before any output, pointing at
// `latere login`.
func TestModelsNeedALoginOrAKeyE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	for _, args := range [][]string{
		{"models", "env"},
		{"models", "env", "--raw"},
		{"models", "list"},
		{"models", "invoke", "--model", "m", "hi"},
		{"models", "key"},
	} {
		for _, state := range []string{"absent", "empty object", "null"} {
			t.Run(strings.Join(args[1:], " ")+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "absent-auth.json")
				if state != "absent" {
					data := []byte(`{}`)
					if state == "null" {
						data = []byte(`null`)
					}
					if err := os.WriteFile(authPath, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, append(args, "--models-url", "http://127.0.0.1:1/v1/models", "--auth-url", "http://127.0.0.1:1")...)
				command.Env = modelsEnv(root, "")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "latere login") {
					t.Errorf("no login = %v; stderr: %s", err, stderr.String())
				}
				if stdout.Len() != 0 {
					t.Errorf("no login produced output: %q", stdout.String())
				}
			})
		}
	}
}
