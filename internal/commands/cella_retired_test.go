// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// retiredCellaRef matches a reference to the retired hosted sandbox API: its
// host, or the variable that pointed at it. The manifest's API group,
// cella.latere.ai/v1beta1, is the core's and is not matched.
var retiredCellaRef = regexp.MustCompile(`cella\.latere\.ai([^/]|$)|//cella\.latere\.ai|SANDBOX_API_URL`)

// No shipped source, and no page a user reads, names the retired API. The
// changelog and the specs keep the history and are not scanned, nor are the
// tests, which name the variable to prove it is ignored.
func TestNothingNamesTheRetiredCellaAPI(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == "specs" || rel == ".git" || strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		scanned := (strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")) ||
			rel == "README.md" || (filepath.Dir(rel) == "docs" && strings.HasSuffix(p, ".md"))
		if !scanned {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			if retiredCellaRef.MatchString(line) {
				t.Errorf("%s:%d names the retired Cella API: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
