// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/upgrade"
)

func TestSkipUpdateCheck(t *testing.T) {
	root := NewRoot("test")
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"upgrade"}, true},
		{[]string{"completion", "zsh"}, true},
		{[]string{"cella", "list"}, false},
		{[]string{"auth", "login"}, false},
	}
	for _, c := range cases {
		cmd, _, err := root.Find(c.args)
		if err != nil {
			t.Fatalf("Find(%v): %v", c.args, err)
		}
		if got := skipUpdateCheck(cmd); got != c.want {
			t.Errorf("skipUpdateCheck(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestUpgradeAutoTogglesConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	out, err := executeForHelp(NewRoot("0.1.0"), "upgrade", "--auto", "off")
	if err != nil {
		t.Fatalf("upgrade --auto off: %v", err)
	}
	if !strings.Contains(out, "Auto-upgrade disabled") {
		t.Errorf("output = %q, want disabled message", out)
	}
	if upgrade.LoadConfig().AutoUpgradeEnabled() {
		t.Error("AutoUpgrade should be disabled after --auto off")
	}

	out, err = executeForHelp(NewRoot("0.1.0"), "upgrade", "--auto", "on")
	if err != nil {
		t.Fatalf("upgrade --auto on: %v", err)
	}
	if !strings.Contains(out, "Auto-upgrade enabled") {
		t.Errorf("output = %q, want enabled message", out)
	}
	if !upgrade.LoadConfig().AutoUpgradeEnabled() {
		t.Error("AutoUpgrade should be enabled after --auto on")
	}
}

func TestUpgradeAutoRejectsBadValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := NewRoot("0.1.0")
	root.SetArgs([]string{"upgrade", "--auto", "maybe"})
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	if err := root.Execute(); err == nil {
		t.Fatal("expected an error for an invalid --auto value")
	}
}

func TestUpgradeHelp(t *testing.T) {
	out, err := executeForHelp(NewRoot("test"), "upgrade", "--help")
	if err != nil {
		t.Fatalf("upgrade --help: %v", err)
	}
	for _, want := range []string{
		"Install a latere release published on GitHub.",
		"latere upgrade v0.2.29    # roll back to a specific release",
		"latere upgrade --check",
		"latere upgrade --auto off",
		"LATERE_NO_UPDATE_CHECK",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q\noutput:\n%s", want, out)
		}
	}
}

// ensure the upgrade command is wired under root.
func TestUpgradeCommandRegistered(t *testing.T) {
	root := NewRoot("test")
	cmd, _, err := root.Find([]string{"upgrade"})
	if err != nil {
		t.Fatalf("Find(upgrade): %v", err)
	}
	if cmd.Name() != "upgrade" {
		t.Fatalf("found %q, want upgrade command", cmd.Name())
	}
}
