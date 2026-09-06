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

func TestLuxEnvReportsOutputFailuresE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, format := range []string{"exports", "raw", "legacy"} {
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
				args := []string{"lux", "env", "--compat", "openai"}
				switch format {
				case "raw":
					args = []string{"lux", "env", "--raw"}
				case "legacy":
					args = []string{"lux", "token"}
				}
				args = append(args, "--token", "synthetic-token", "--lux-url", "https://lux.example")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_LUX_TOKEN=", "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = file, &stderr
				if mode == "failed stderr" {
					command.Stdout, command.Stderr = &stdout, file
				}
				err = command.Run()
				wantFailure := mode == "failed stdout" || (mode == "failed stderr" && format != "legacy")
				if wantFailure {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("output failure reported success: %v: %s", err, stderr.String())
					}
					if mode == "failed stdout" && !strings.Contains(stderr.String(), "write") {
						t.Errorf("missing write error: %s", stderr.String())
					}
				} else if err != nil {
					t.Errorf("valid output failed: %v: %s", err, stderr.String())
				}
				want := "synthetic-token\n"
				if format == "exports" {
					want = "export OPENAI_BASE_URL=https://lux.example/compat/openai/v1\nexport OPENAI_API_KEY=synthetic-token\n"
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
