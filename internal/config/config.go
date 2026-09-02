// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package config resolves the CLI's per-user config directory. It is a leaf
// package with no internal imports so api, upgrade, and tunnel can all share
// one definition of where latere keeps its state, instead of each carrying a
// copy of the same XDG resolution.
package config

import (
	"os"
	"path/filepath"
)

// Dir returns $XDG_CONFIG_HOME/latere, falling back to ~/.config/latere.
// It returns "" when neither resolves (no XDG_CONFIG_HOME and no home dir),
// matching the degrade contract callers rely on: api treats "" as not-logged-in,
// upgrade and tunnel treat "" as skip-persist.
func Dir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "latere")
}

// Path joins elem onto Dir(). It returns "" when Dir() is "" so the empty
// degrade path propagates unchanged.
func Path(elem ...string) string {
	d := Dir()
	if d == "" {
		return ""
	}
	return filepath.Join(append([]string{d}, elem...)...)
}
