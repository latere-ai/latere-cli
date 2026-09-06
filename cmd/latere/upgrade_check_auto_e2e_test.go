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

func TestUpgradeCheckAutoE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, auto := range []string{"on", "off"} {
		for _, check := range []bool{true, false} {
			for _, exists := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/check=%t/existing=%t", auto, check, exists), func(t *testing.T) {
					dir := t.TempDir()
					config := filepath.Join(dir, "latere", "config.json")
					before := []byte(`{"auto_upgrade":true}`)
					if exists {
						if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(config, before, 0600); err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, "upgrade", "--auto", auto, fmt.Sprintf("--check=%t", check))
					command.Env = append(os.Environ(), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					after, readErr := os.ReadFile(config)
					if !check {
						var saved struct {
							AutoUpgrade *bool `json:"auto_upgrade"`
						}
						if err != nil || readErr != nil || json.Unmarshal(after, &saved) != nil || saved.AutoUpgrade == nil || *saved.AutoUpgrade != (auto == "on") || diagnostic.Len() != 0 {
							t.Errorf("setting failed: error=%v config=%q stderr=%q", err, after, diagnostic.String())
						}
						return
					}
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "--auto cannot be combined with --check") {
						t.Errorf("conflict: error=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
					}
					if exists {
						if readErr != nil || !bytes.Equal(after, before) {
							t.Errorf("configuration changed: %q error=%v", after, readErr)
						}
					} else if !errors.Is(readErr, os.ErrNotExist) {
						t.Errorf("configuration created: %q error=%v", after, readErr)
					}
				})
			}
		}
	}
}
