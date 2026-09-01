package commands

import "testing"

func TestSafeArchivePath(t *testing.T) {
	cases := map[string]bool{
		"a/b.txt":     true,
		"./a/b.txt":   true,
		"..hidden":    true,
		"":            false,
		".":           false,
		"./":          false,
		"/etc/passwd": false,
		"../x":        false,
		"a/../../x":   false,
		"a/..":        false,
		"a/./b":       false,
		"a\x00b":      false,
	}
	for name, want := range cases {
		if got := safeArchivePath(name); got != want {
			t.Errorf("safeArchivePath(%q) = %v, want %v", name, got, want)
		}
	}
}
