package config

import (
	"path/filepath"
	"testing"
)

func TestDirUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := Dir(), filepath.Join("/xdg", "latere"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if got, want := Path("token.json"), filepath.Join("/xdg", "latere", "token.json"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/user")
	if got, want := Dir(), filepath.Join("/home/user", ".config", "latere"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

// TestEmptyWhenNoDir locks the degrade contract callers rely on: when no
// config dir can be resolved, Path returns "" rather than a bare relative path.
func TestEmptyWhenNoDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	// On some platforms UserHomeDir reads other vars; only assert the
	// invariant when it genuinely cannot resolve a home.
	if Dir() != "" {
		t.Skip("home resolvable on this platform; degrade path not exercised")
	}
	if got := Path("token.json"); got != "" {
		t.Errorf("Path() with no dir = %q, want empty", got)
	}
}
