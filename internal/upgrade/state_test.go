package upgrade

import (
	"testing"
	"time"
)

func TestConfigDefaultsToAutoUpgradeOn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// No config file yet: auto-upgrade is on by default.
	if c := LoadConfig(); !c.AutoUpgradeEnabled() {
		t.Fatal("fresh config should default AutoUpgradeEnabled to true")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	off := false
	if err := SaveConfig(Config{AutoUpgrade: &off}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if c := LoadConfig(); c.AutoUpgradeEnabled() {
		t.Fatal("explicit off should persist as disabled")
	}

	on := true
	if err := SaveConfig(Config{AutoUpgrade: &on}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if c := LoadConfig(); !c.AutoUpgradeEnabled() {
		t.Fatal("explicit on should persist as enabled")
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Missing state reads as the zero value, which is stale.
	if got := loadState(); got.LatestVersion != "" {
		t.Fatalf("fresh state LatestVersion = %q, want empty", got.LatestVersion)
	}
	want := checkState{CheckedAt: time.Now().Truncate(time.Second), LatestVersion: "v0.3.0"}
	if err := saveState(want); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got := loadState()
	if got.LatestVersion != want.LatestVersion {
		t.Errorf("LatestVersion = %q, want %q", got.LatestVersion, want.LatestVersion)
	}
	if !got.CheckedAt.Equal(want.CheckedAt) {
		t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, want.CheckedAt)
	}
}

func TestStale(t *testing.T) {
	now := time.Now()
	if !stale(checkState{}, now) {
		t.Error("zero state should be stale")
	}
	if !stale(checkState{CheckedAt: now.Add(-25 * time.Hour)}, now) {
		t.Error("25h-old state should be stale")
	}
	if stale(checkState{CheckedAt: now.Add(-1 * time.Hour)}, now) {
		t.Error("1h-old state should be fresh")
	}
}
