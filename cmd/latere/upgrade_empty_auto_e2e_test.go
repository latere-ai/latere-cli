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

func TestUpgradeEmptyAutoE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, auto := range [][]string{{"--auto="}, {"--auto", ""}} {
		t.Run(strings.Join(auto, " "), func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			// An explicit version and --check keep the old implementation
			// offline and prevent executable replacement while reproducing
			// its silent fallback to the upgrade path.
			args := append([]string{"upgrade", "v9.9.9", "--check"}, auto...)
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(os.Environ(), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			err := command.Run()
			if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "--auto cannot be combined with --check") {
				t.Errorf("error=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "latere", "config.json")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("configuration created: %v", err)
			}
		})
	}
}
