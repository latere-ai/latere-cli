// Package reviews resolves where `latere review` writes its review logs. It
// mirrors internal/config's XDG resolution, but reviews are transient state
// (not config), so they live under $XDG_STATE_HOME/latere/reviews, namespaced
// by the reviewed repo so a project's runs are findable and prunable. Keeping
// the path convention in one leaf package means the command and its retention
// pass agree on the layout.
package reviews

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Retention caps for the global review-log dir. A per-repo working-tree dir was
// cleaned when the repo was cleaned; the shared global dir is not, so it grows
// unbounded without a prune. The caps are deliberately conservative: they only
// bound obvious runaway growth and never touch a user-chosen --state-dir.
const (
	// MaxSessions keeps at most this many sessions per repo (newest win).
	MaxSessions = 50
	// MaxAge deletes sessions older than this regardless of count.
	MaxAge = 30 * 24 * time.Hour
)

// stateHome returns $XDG_STATE_HOME, falling back to ~/.local/state, or "" when
// neither resolves. It matches the degrade contract config.Dir uses: "" means
// the caller should fall back rather than guess.
func stateHome() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return dir
}

// Dir returns $XDG_STATE_HOME/latere/reviews/<repo-key>, falling back to
// ~/.local/state/latere/reviews/<repo-key>. The engine writes sessions/<id>/
// under this path. It returns "" when no state home resolves, so the caller can
// degrade (e.g. fall back to an explicit --state-dir) instead of writing to a
// guessed location. repoRoot is the reviewed repo's git toplevel path.
func Dir(repoRoot string) string {
	home := stateHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "latere", "reviews", RepoKey(repoRoot))
}

// RepoKey derives a filesystem-safe, stable, collision-resistant key from a
// repo's absolute path: a readable slug of the basename plus a short hash of
// the full path. Two repos with the same basename get distinct keys, and the
// key is stable across runs so a project's reviews accumulate under one folder.
func RepoKey(repoRoot string) string {
	sum := sha256.Sum256([]byte(repoRoot))
	short := hex.EncodeToString(sum[:])[:8]
	slug := slugify(filepath.Base(repoRoot))
	if slug == "" {
		return short
	}
	return slug + "-" + short
}

// slugify lowercases and replaces any non [a-z0-9._-] run with a single dash,
// trimming leading/trailing dashes, so a repo basename is safe as a path
// segment on any filesystem.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// Prune enforces the retention caps under dir/sessions/: it deletes sessions
// older than MaxAge and, of what remains, keeps only the MaxSessions newest by
// mtime. It is best-effort (errors are ignored) and a no-op when the sessions
// dir does not yet exist, so it is safe to call before every run.
func Prune(dir string) {
	prune(filepath.Join(dir, "sessions"), MaxSessions, MaxAge, time.Now())
}

func prune(sessionsDir string, maxKeep int, maxAge time.Duration, now time.Time) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return
	}
	type sess struct {
		path string
		mod  time.Time
	}
	var sessions []sess
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sessions = append(sessions, sess{path: filepath.Join(sessionsDir, e.Name()), mod: info.ModTime()})
	}
	// Newest first, so anything past maxKeep is the oldest tail.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].mod.After(sessions[j].mod) })
	for i, s := range sessions {
		if i >= maxKeep || now.Sub(s.mod) > maxAge {
			_ = os.RemoveAll(s.path)
		}
	}
}
