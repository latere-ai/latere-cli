package reviews

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDirUsesXDGStateHome asserts the override branch: when XDG_STATE_HOME is
// set, Dir resolves under it, namespaced by the repo key.
func TestDirUsesXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	repo := "/Users/dev/myrepo"
	got := Dir(repo)
	want := filepath.Join("/xdg/state", "latere", "reviews", RepoKey(repo))
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

// TestDirFallsBackToLocalState asserts the fallback branch: with no
// XDG_STATE_HOME, Dir resolves under ~/.local/state.
func TestDirFallsBackToLocalState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := "/Users/dev/myrepo"
	got := Dir(repo)
	want := filepath.Join(home, ".local", "state", "latere", "reviews", RepoKey(repo))
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

// TestRepoKeyStableAndDistinct: the key is stable for one path, readable
// (carries the basename slug), and distinct for same-basename repos.
func TestRepoKeyStableAndDistinct(t *testing.T) {
	a := RepoKey("/Users/dev/myrepo")
	if a != RepoKey("/Users/dev/myrepo") {
		t.Error("RepoKey is not stable across calls")
	}
	if a[:7] != "myrepo-" {
		t.Errorf("RepoKey = %q, want it prefixed by the basename slug", a)
	}
	if a == RepoKey("/Users/other/myrepo") {
		t.Error("same basename under different paths must yield distinct keys")
	}
}

// TestPruneKeepsNewestAndDropsOldAndAged builds staggered session dirs and
// asserts the retention pass keeps the newest MaxSessions and drops both the
// overflow tail and anything older than MaxAge.
func TestPruneKeepsNewestAndDropsOldAndAged(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	mk := func(name string, mod time.Time) string {
		p := filepath.Join(sessionsDir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// keepNewest: within age and within the newest-3 window.
	keep := []string{
		mk("k0", now.Add(-1*time.Hour)),
		mk("k1", now.Add(-2*time.Hour)),
		mk("k2", now.Add(-3*time.Hour)),
	}
	// overflow: recent but pushed past maxKeep=3 by the three above.
	overflow := mk("overflow", now.Add(-4*time.Hour))
	// aged: within the newest window by count is irrelevant; it's beyond maxAge.
	aged := mk("aged", now.Add(-100*24*time.Hour))

	prune(sessionsDir, 3, MaxAge, now)

	for _, p := range keep {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s kept, got %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(overflow); !os.IsNotExist(err) {
		t.Errorf("expected overflow session pruned, stat err = %v", err)
	}
	if _, err := os.Stat(aged); !os.IsNotExist(err) {
		t.Errorf("expected aged session pruned, stat err = %v", err)
	}
}

// TestPruneNoSessionsDirIsNoop: Prune on a dir with no sessions/ subdir must
// not error or create anything (safe to call before the first run).
func TestPruneNoSessionsDirIsNoop(t *testing.T) {
	dir := t.TempDir()
	Prune(dir) // must not panic
	if _, err := os.Stat(filepath.Join(dir, "sessions")); !os.IsNotExist(err) {
		t.Errorf("Prune created sessions dir, err = %v", err)
	}
}
