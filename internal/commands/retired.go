// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// The command words and variables the CLI no longer has (specs 011 and
// 015). Each old word is refused for one release with its replacement, so
// a script that still uses it fails on its first run with the new name in
// the error, and is removed in the release after. This file is the one
// place the old words are written outside the tests; the scan in
// capability_scan_test.go holds every other shipped source to the new ones.

// retiredCommands maps each retired top-level word to the sentence that
// names its replacement.
var retiredCommands = map[string]string{
	"eval": "'latere eval' was the command of the retired Eval service; 'latere evals' reads your suites and experiments in Evals",
}

// newRetiredCmd is a hidden command for a retired word. It takes any
// arguments and flags without parsing them, so whatever followed the word,
// the answer is the replacement.
func newRetiredCmd(word string) *cobra.Command {
	return &cobra.Command{
		Use:                word,
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%s", retiredCommands[word])
		},
	}
}

// renamedVariables maps each retired environment variable to the one that
// replaced it.
var renamedVariables = map[string]string{
	"EVAL_API_URL":     envEvalsURL,
	"EVAL_ADMIN_TOKEN": envEvalsToken,
}

// renamedEnvError is a retired variable set without its replacement. It is
// an error rather than ignored: a script that pointed the CLI at another
// address through the old name would otherwise reach production.
type renamedEnvError struct{ old, current string }

func (e *renamedEnvError) Error() string {
	return fmt.Sprintf("%s is now %s; set %s instead", e.old, e.current, e.current)
}

// capabilityEnv reads the variable name, and refuses when only the retired
// variable it replaced is set. With both set, name wins.
func capabilityEnv(name string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	for old, current := range renamedVariables {
		if current == name && os.Getenv(old) != "" {
			return "", &renamedEnvError{old: old, current: name}
		}
	}
	return "", nil
}
