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

func TestPrintTokenReportsOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, prefix := range []string{"", "auth"} {
		for _, mode := range []string{"writable", "read-only"} {
			t.Run(prefix+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				tokenPath, outputPath := filepath.Join(root, "token.json"), filepath.Join(root, "output")
				const tokenData = `{"access_token":"synthetic-token"}`
				if err := os.WriteFile(tokenPath, []byte(tokenData), 0o600); err != nil {
					t.Fatal(err)
				}
				const previous = "existing output\n"
				if err := os.WriteFile(outputPath, []byte(previous), 0o600); err != nil {
					t.Fatal(err)
				}
				flags := os.O_RDONLY
				if mode == "writable" {
					flags = os.O_WRONLY | os.O_APPEND
				}
				file, err := os.OpenFile(outputPath, flags, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				args := []string{"print-token"}
				if prefix != "" {
					args = append([]string{prefix}, args...)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"),
					"XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = file, &diagnostic
				err = command.Run()
				want := previous
				if mode == "writable" {
					want += "synthetic-token\n"
					if err != nil || diagnostic.Len() != 0 {
						t.Errorf("valid output failed: %v: %s", err, diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write") {
					t.Errorf("failed output returned %v: %s", err, diagnostic.String())
				}
				if strings.Contains(diagnostic.String(), "synthetic-token") {
					t.Error("write diagnostic exposed token contents")
				}
				if got, err := os.ReadFile(outputPath); err != nil || string(got) != want {
					t.Errorf("output contents = %q (%v), want %q", got, err, want)
				}
				if got, err := os.ReadFile(tokenPath); err != nil || string(got) != tokenData {
					t.Errorf("printing changed the saved token: %v", err)
				}
			})
		}
	}
}
