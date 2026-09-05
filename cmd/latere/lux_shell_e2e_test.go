// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLuxEnvExportsLiteralShellValuesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, token, url, route string
		raw, bad                bool
	}{
		{name: "ordinary", token: "abc.DEF-123_=="},
		{name: "spaces", token: "key with spaces"},
		{name: "quotes", token: "key'with\"quotes"},
		{name: "newline", token: "first\nsecond"},
		{name: "substitution", token: `key$(printf injected > "$MARKER")end`},
		{name: "parameter expansion", token: "key${SHELL_VALUE}end"},
		{name: "URL characters", token: "key", url: "https://lux.example/${SHELL_VALUE}?a=1&b=2"},
		{name: "server route", token: "key", route: `/custom$(printf injected > "$MARKER")`},
		{name: "raw stays literal", token: `key$(printf injected > "$MARKER")end`, raw: true},
		{name: "NUL credential", token: "key\x00end", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			authPath := filepath.Join(root, "auth-token.json")
			data, _ := json.Marshal(map[string]string{"access_token": tc.token})
			if err := os.WriteFile(authPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			base := tc.url
			if base == "" {
				base = "https://lux.example"
			}
			args := []string{"lux", "env", "--compat", "openai"}
			wantBase := base + "/compat/openai/v1"
			if tc.route != "" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/lux/v1/providers" {
						t.Errorf("unexpected catalog request: %s", r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]string{{"id": "custom", "dialect": "openai-chat", "default_route_prefix": tc.route}}})
				}))
				defer server.Close()
				base = server.URL
				wantBase = base + tc.route + "/v1"
				args = []string{"lux", "env", "custom", "--token", tc.token}
			}
			args = append(args, "--lux-url", base)
			if tc.raw {
				args = append(args, "--raw")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(os.Environ(), "LATERE_LUX_TOKEN=", "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if tc.bad {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "NUL") || stdout.Len() != 0 {
					t.Errorf("NUL export=%v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("lux env: %v; %s", err, stderr.String())
			}
			if tc.raw {
				if stdout.String() != tc.token+"\n" {
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
					want := wantBase + "\x00" + tc.token
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
