// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/upgrade"
)

func TestUpgradeAutoOutput(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, tc := range []struct{ setting, message string }{
		{"on", "Auto-upgrade enabled. latere will update itself on the next run when a new release is available.\n"},
		{"off", "Auto-upgrade disabled.\n"},
	} {
		for _, limit := range []int{-1, 0, 3} {
			t.Run(fmt.Sprintf("%s/limit=%d", tc.setting, limit), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				previous := tc.setting != "on"
				if err := upgrade.SaveConfig(upgrade.Config{AutoUpgrade: &previous}); err != nil {
					t.Fatal(err)
				}
				out := &evalOutputWriter{remaining: limit}
				if limit >= 0 {
					out.err = io.ErrClosedPipe
				}
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				root.SetArgs([]string{"upgrade", "--auto", tc.setting})
				err := root.Execute()
				want := tc.message
				if limit >= 0 {
					want = want[:limit]
					if !errors.Is(err, io.ErrClosedPipe) {
						t.Errorf("error=%v, want wrapped write failure", err)
					}
					if err == nil || !strings.Contains(err.Error(), "preference was saved") {
						t.Errorf("error does not explain saved state: %v", err)
					}
				} else if err != nil {
					t.Errorf("successful output: %v", err)
				}
				if out.String() != want {
					t.Errorf("output=%q want=%q", out.String(), want)
				}
				saved := upgrade.LoadConfig()
				if saved.AutoUpgrade == nil || *saved.AutoUpgrade != (tc.setting == "on") {
					t.Errorf("preference not saved: %+v", saved)
				}
			})
		}
	}
}
