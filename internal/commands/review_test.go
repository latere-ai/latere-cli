// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"latere.ai/x/topos/adversarial/input"
)

// jwtWithExp builds a minimal three-segment JWT whose payload carries the
// given exp claim, so decodeJWTClaims parses it. The signature segment is a
// placeholder (decodeJWTClaims never verifies it).
func jwtWithExp(exp int64) string {
	payload, _ := json.Marshal(map[string]any{"exp": exp})
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

// TestReviewFlagDefaults pins the documented defaults so a careless flag
// edit can't silently change the command's behavior.
func TestReviewFlagDefaults(t *testing.T) {
	cmd := newReviewCmd()
	cases := []struct {
		flag string
		want string
	}{
		{"dir", "."},
		{"forks", "1"},
		{"max-rounds", "4"},
		{"cost-cap", "50000"},
		{"model", "claude-sonnet-4-6"},
		{"proposer-timeout", "5m0s"},
		{"session", ""},
		{"state-dir", ""},
	}
	for _, tc := range cases {
		f := cmd.Flags().Lookup(tc.flag)
		if f == nil {
			t.Errorf("missing flag --%s", tc.flag)
			continue
		}
		if f.DefValue != tc.want {
			t.Errorf("--%s default = %q, want %q", tc.flag, f.DefValue, tc.want)
		}
	}
}

// TestEnsureBearerFresh covers the expiry preflight: an expired JWT errors,
// a future one passes, and non-JWT / no-exp tokens are skipped (Lux stays the
// authority).
func TestEnsureBearerFresh(t *testing.T) {
	now := time.Now().Unix()
	if err := ensureBearerFresh(jwtWithExp(now - 60)); err == nil {
		t.Error("expired token: want error, got nil")
	}
	if err := ensureBearerFresh(jwtWithExp(now + 3600)); err != nil {
		t.Errorf("fresh token: want nil, got %v", err)
	}
	if err := ensureBearerFresh("opaque-not-a-jwt"); err != nil {
		t.Errorf("opaque token: want nil (skip), got %v", err)
	}
	// JWT-shaped but no exp claim -> skip.
	noExp := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".s"
	if err := ensureBearerFresh(noExp); err != nil {
		t.Errorf("no-exp token: want nil (skip), got %v", err)
	}
}

// TestMostRecentSessionPicksNewest builds a fake ~/.claude/projects tree
// for a cwd and checks that the newest .jsonl by mtime wins, that its
// session ID is the basename without extension, and that non-.jsonl
// entries are ignored.
func TestMostRecentSessionPicksNewest(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/dev/myrepo"
	dir := filepath.Join(home, ".claude", "projects", input.EncodeCwd(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name string, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	write("old.jsonl", base)
	write("newest.jsonl", base.Add(30*time.Minute))
	write("middle.jsonl", base.Add(10*time.Minute))
	write("notes.txt", base.Add(time.Hour)) // newer but not a transcript

	id, path, err := mostRecentSession(home, cwd)
	if err != nil {
		t.Fatalf("mostRecentSession: %v", err)
	}
	if id != "newest" {
		t.Errorf("session id = %q, want %q", id, "newest")
	}
	if path != filepath.Join(dir, "newest.jsonl") {
		t.Errorf("path = %q, want %q", path, filepath.Join(dir, "newest.jsonl"))
	}
}

// TestMostRecentSessionNoSessions returns a clear, actionable error when
// the project directory is missing or empty (no .jsonl files).
func TestMostRecentSessionNoSessions(t *testing.T) {
	home := t.TempDir()

	// Missing project dir.
	if _, _, err := mostRecentSession(home, "/Users/dev/nope"); err == nil {
		t.Fatal("expected error for missing project dir, got nil")
	}

	// Present but empty (only a non-transcript file).
	cwd := "/Users/dev/empty"
	dir := filepath.Join(home, ".claude", "projects", input.EncodeCwd(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mostRecentSession(home, cwd); err == nil {
		t.Fatal("expected error for empty project dir, got nil")
	}
}
