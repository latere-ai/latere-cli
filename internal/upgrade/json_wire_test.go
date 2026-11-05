// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigIsReadAsAPersonEditedIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	p := configPath()
	if p == "" {
		t.Skip("no config directory")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"Auto_Upgrade":true,"auto_upgrade":false,"auto_upgrade":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := LoadConfig(); c.AutoUpgradeEnabled() {
		t.Errorf("LoadConfig = %+v, want automatic upgrades off", c)
	}
}

func TestCheckStateIsWrittenIndentedAndOmitsWhatIsZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	if statePath() == "" {
		t.Skip("no config directory")
	}
	at := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	if err := saveState(checkState{CheckedAt: at, LatestVersion: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"checked_at\": \"2026-10-14T09:30:00Z\",\n  \"latest_version\": \"v1.2.3\"\n}"
	if string(got) != want {
		t.Errorf("update-check.json = %q, want %q", got, want)
	}
	if s := loadState(); !s.CheckedAt.Equal(at) || s.LatestVersion != "v1.2.3" {
		t.Errorf("loadState = %+v", s)
	}
}
