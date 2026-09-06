// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/upgrade"
)

func TestUpgradeCheckAutoConflict(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, auto := range []string{"on", "off"} {
		for _, check := range []string{"--check", "--check=true", "--check=false"} {
			for _, exists := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/existing=%t", auto, check, exists), func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv("XDG_CONFIG_HOME", dir)
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
					var out bytes.Buffer
					root := NewRoot("test")
					root.SetOut(&out)
					root.SetErr(io.Discard)
					root.SetArgs([]string{"upgrade", check, "--auto", auto})
					err := root.Execute()
					if check == "--check=false" {
						if err != nil || upgrade.LoadConfig().AutoUpgradeEnabled() != (auto == "on") || !strings.Contains(out.String(), "Auto-upgrade") {
							t.Errorf("setting failed: error=%v output=%q", err, out.String())
						}
						return
					}
					if err == nil || !strings.Contains(err.Error(), "--auto cannot be combined with --check") || out.Len() != 0 {
						t.Errorf("conflict: error=%v output=%q", err, out.String())
					}
					after, readErr := os.ReadFile(config)
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
