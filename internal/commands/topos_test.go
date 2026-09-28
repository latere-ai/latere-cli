// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestToposCommandRegisteredInRoot verifies that 'latere topos' is reachable
// through the root command tree.
func TestToposCommandRegisteredInRoot(t *testing.T) {
	root := NewRoot("test")
	var found bool
	for _, cmd := range root.Commands() {
		if cmd.Name() == "topos" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("'topos' command not registered in root")
	}
}

func TestToposHelpText(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "topos",
			args: []string{"topos", "--help"},
			want: []string{"agent loop", "--local", "retired"},
		},
		{
			name: "topos login",
			args: []string{"topos", "login", "--help"},
			want: []string{"model provider", "--local"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := executeForHelp(NewRoot("test"), tc.args...)
			if err != nil {
				t.Fatalf("help command failed: %v\noutput:\n%s", err, got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("help output missing %q\noutput:\n%s", want, got)
				}
			}
		})
	}
}

// TestToposHostedSubcommandsGone pins that the hosted platform's commands are
// not registered: 'latere topos' carries only the login picker.
func TestToposHostedSubcommandsGone(t *testing.T) {
	cmd := newToposCmd()
	var names []string
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	if len(names) != 1 || names[0] != "login" {
		t.Fatalf("topos subcommands = %v, want [login]", names)
	}
	if cmd.Flags().Lookup("api-url") != nil {
		t.Fatal("topos still accepts --api-url")
	}
}

// TestToposWithoutLocalRejected pins that the root command without --local
// fails with the retirement error and runs nothing, whether or not a prompt
// was given.
func TestToposWithoutLocalRejected(t *testing.T) {
	for _, args := range [][]string{
		{"topos"},
		{"topos", "-p", "explain this repo"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := NewRoot("test")
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(args)
			err := root.Execute()
			if !errors.Is(err, errToposHostedRetired) {
				t.Fatalf("error = %v, want the hosted retirement error", err)
			}
			if !strings.Contains(err.Error(), "latere topos --local") {
				t.Errorf("error %q does not point the user at 'latere topos --local'", err)
			}
		})
	}
}
