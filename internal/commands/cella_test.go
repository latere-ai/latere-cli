// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import "testing"

func TestSafeArchivePath(t *testing.T) {
	cases := map[string]bool{
		"a/b.txt":     true,
		"./a/b.txt":   true,
		"..hidden":    true,
		"empty/":      true,
		"a/b/":        true,
		"./a/b/":      true,
		"":            false,
		".":           false,
		"./":          false,
		"/etc/passwd": false,
		"../x":        false,
		"a/../../x":   false,
		"a/..":        false,
		"a/./b":       false,
		"a\x00b":      false,
		"/":           false,
		"../":         false,
		"a/../":       false,
		"a/./":        false,
		"a//":         false,
		"a//b/":       false,
		"././a/":      false,
	}
	for name, want := range cases {
		if got := safeArchivePath(name); got != want {
			t.Errorf("safeArchivePath(%q) = %v, want %v", name, got, want)
		}
	}
}
