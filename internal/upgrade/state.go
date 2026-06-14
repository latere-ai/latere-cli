package upgrade

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// checkInterval is how often the CLI refreshes its view of the latest
// release. Notices are served from the cache between refreshes so the hot
// path never blocks on the network.
const checkInterval = 24 * time.Hour

// configDir resolves $XDG_CONFIG_HOME/latere (falling back to ~/.config/latere),
// matching the token storage layout in internal/api.
func configDir() string {
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

// Config is the user's persistent CLI configuration
// (~/.config/latere/config.json). It is deliberately small and additive so
// future settings can join without a migration.
type Config struct {
	AutoUpgrade bool `json:"auto_upgrade"`
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

// checkState caches the result of the most recent release lookup.
type checkState struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
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

// writeFileAtomic writes data to a temp file in the same directory and renames
// it into place, so a process exiting mid-write never leaves a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
