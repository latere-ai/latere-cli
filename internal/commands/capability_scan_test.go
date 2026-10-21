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

// retiredName matches a command or variable named after an open core rather
// than the capability it serves (spec 011), as a person would type it. This
// is the one place the old names are written. The manifest's API group
// cella.latere.ai, the token audience "cella" and Go identifiers are not
// matched.
var retiredName = regexp.MustCompile(`latere (cella|topos|sandbox|review|app)\b|LATERE_(CELLA_URL|CELLA_TOKEN|TOPOS_PROVIDER_FILE|APP_URL|APP_TOKEN)\b|topos-provider\.json`)

// No help, example, error, README or docs page names a retired command or
// variable, except the provider file's move. The changelog and the specs
// keep the history and are not scanned, nor are the tests.
func TestNothingNamesARetiredCommand(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		// The legacy file name is read once to move the saved choice.
		filepath.Join("internal", "commands", "topos_provider.go"): true,
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == "specs" || strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		scanned := (strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")) ||
			rel == "README.md" || rel == "CONTRIBUTING.md" || (filepath.Dir(rel) == "docs" && strings.HasSuffix(p, ".md"))
		if !scanned || allowed[rel] {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			if retiredName.MatchString(line) {
				t.Errorf("%s:%d names a retired command or variable: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
