package upgrade

import (
	"testing"
	"time"
)

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if c := LoadConfig(); c.AutoUpgrade {
		t.Fatal("fresh config should have AutoUpgrade=false")
	}
	if err := SaveConfig(Config{AutoUpgrade: true}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if c := LoadConfig(); !c.AutoUpgrade {
		t.Fatal("AutoUpgrade should persist as true")
	}
	if err := SaveConfig(Config{AutoUpgrade: false}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if c := LoadConfig(); c.AutoUpgrade {
		t.Fatal("AutoUpgrade should persist as false")
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
