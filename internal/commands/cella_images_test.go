// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"strings"
	"testing"
)

// The platform's catalog offers base, gui and science (its posture page and
// image catalog), and the help of every command that names an image names
// all three.
func TestTheHelpNamesEveryCatalogImage(t *testing.T) {
	for _, args := range [][]string{
		{"environments", "--help"},
		{"environments", "apply", "--help"},
		{"environments", "run", "--help"},
	} {
		got, err := executeForHelp(NewRoot("test"), args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		for _, image := range []string{"base", "gui", "science"} {
			if !strings.Contains(got, image) {
				t.Errorf("%v help does not name the %s image\n%s", args, image, got)
			}
		}
	}
}
