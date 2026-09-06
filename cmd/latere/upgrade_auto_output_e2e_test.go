// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpgradeAutoOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct{ setting, message string }{
		{"on", "Auto-upgrade enabled. latere will update itself on the next run when a new release is available.\n"},
		{"off", "Auto-upgrade disabled.\n"},
	} {
		for _, writable := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/writable=%t", tc.setting, writable), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "output")
				if err := os.WriteFile(path, []byte("previous\n"), 0600); err != nil {
					t.Fatal(err)
				}
				mode := os.O_RDONLY
				if writable {
					mode = os.O_WRONLY | os.O_APPEND
				}
				out, err := os.OpenFile(path, mode, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = out.Close() }()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "upgrade", "--auto", tc.setting)
				command.Env = append(os.Environ(), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = out, &diagnostic
				err = command.Run()
				want := "previous\n"
				if writable {
					want += tc.message
					if err != nil || diagnostic.Len() != 0 {
						t.Errorf("successful output: error=%v stderr=%q", err, diagnostic.String())
					}
				} else {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("failed output: error=%v stderr=%q", err, diagnostic.String())
					}
					if !strings.Contains(diagnostic.String(), "preference was saved") {
						t.Errorf("missing saved state diagnostic: %q", diagnostic.String())
					}
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Errorf("output=%q want=%q error=%v", got, want, err)
				}
				config, err := os.ReadFile(filepath.Join(dir, "latere", "config.json"))
				if err != nil {
					t.Fatal(err)
				}
				var saved struct {
					AutoUpgrade *bool `json:"auto_upgrade"`
				}
				if err := json.Unmarshal(config, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.AutoUpgrade == nil || *saved.AutoUpgrade != (tc.setting == "on") {
					t.Errorf("preference not saved: %s", config)
				}
			})
		}
	}
}
