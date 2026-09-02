// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"latere.ai/x/pkg/atomicfile"

	"github.com/latere-ai/latere-cli/internal/config"
)

// checkInterval is how often the CLI refreshes its view of the latest
// release. Notices are served from the cache between refreshes so the hot
// path never blocks on the network.
const checkInterval = 24 * time.Hour

// configDir resolves $XDG_CONFIG_HOME/latere (falling back to ~/.config/latere),
// matching the token storage layout in internal/api.
func configDir() string {
	return config.Dir()
}

// Config is the user's persistent CLI configuration
// (~/.config/latere/config.json). It is deliberately small and additive so
// future settings can join without a migration.
//
// AutoUpgrade is a pointer so that "never set" (nil) is distinct from an
// explicit false: auto-upgrade is on by default, and only an explicit
// `latere upgrade --auto off` turns it off.
type Config struct {
	AutoUpgrade *bool `json:"auto_upgrade,omitempty"`
}

// AutoUpgradeEnabled reports the effective setting, defaulting to true when
// the user has never chosen.
func (c Config) AutoUpgradeEnabled() bool {
	return c.AutoUpgrade == nil || *c.AutoUpgrade
}

func configPath() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "config.json")
}

// LoadConfig reads config.json, returning zero values when it is missing or
// unreadable. Configuration is best-effort: a corrupt file must never break
// an unrelated command.
func LoadConfig() Config {
	var c Config
	p := configPath()
	if p == "" {
		return c
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

// SaveConfig persists config.json with 0600 perms, creating the directory if
// needed.
func SaveConfig(c Config) error {
	p := configPath()
	if p == "" {
		return errors.New("cannot determine config path")
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p, b, 0o600)
}

// checkState caches the result of the most recent release lookup and when the
// user was last reminded, so the notice repeats at most once per interval per
// version rather than on every command.
type checkState struct {
	CheckedAt       time.Time `json:"checked_at"`
	LatestVersion   string    `json:"latest_version"`
	NotifiedVersion string    `json:"notified_version,omitempty"`
	NotifiedAt      time.Time `json:"notified_at,omitzero"`
}

// shouldNotify reports whether to print the upgrade notice now: always for a
// version not yet announced, otherwise at most once per checkInterval.
func (s checkState) shouldNotify(latest string, now time.Time) bool {
	if s.NotifiedVersion != latest {
		return true
	}
	return now.Sub(s.NotifiedAt) >= checkInterval
}

func statePath() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "update-check.json")
}

// loadState reads update-check.json, returning the zero value (which reads as
// stale) when it is missing or unreadable.
func loadState() checkState {
	var s checkState
	p := statePath()
	if p == "" {
		return s
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func saveState(s checkState) error {
	p := statePath()
	if p == "" {
		return errors.New("cannot determine state path")
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p, b, 0o600)
}

// stale reports whether the cached check is old enough to refresh.
func stale(s checkState, now time.Time) bool {
	return now.Sub(s.CheckedAt) >= checkInterval
}

// writeFileAtomic creates the state directory and replaces path in one
// rename, so a process exiting mid-write never leaves a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, data, perm)
}
