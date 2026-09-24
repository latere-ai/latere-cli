// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adversarial "latere.ai/x/topos/adversarial"
	"latere.ai/x/topos/adversarial/critic"
	"latere.ai/x/topos/adversarial/input"
)

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
		{"model", "anthropic/claude-sonnet-4.6"},
		{"proposer-timeout", "5m0s"},
		{"session", ""},
		{"state-dir", ""},
		{"models-url", ""},
		{"auth-url", ""},
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
	for _, gone := range []string{"lux-url", "token"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("--%s is still a flag", gone)
		}
	}
}

// TestReviewCriticCallsTheDoor is criterion 4 for review: a critic round
// sends its model call to the core's OpenAI door with the model key and the
// catalog name of the default critic model, once the core accepted the key.
func TestReviewCriticCallsTheDoor(t *testing.T) {
	w := newKeyWorld(t, "")
	t.Setenv("AUTH_URL", w.srv.URL)
	key := modelKeySource(w.modelsURL(), "")
	if _, err := key(t.Context()); err != nil {
		t.Fatalf("key source: %v", err)
	}
	critics := critic.NewCriticFactory(critic.Config{
		Model: reviewCriticModel(defaultCatalogModel, w.modelsURL(), key),
	})
	res, err := critics(1).Round(t.Context(), adversarial.CriticInput{
		AspectName: "correctness", SystemPrompt: "you are a critic", CriticIndex: 1, Round: 1,
		TaskContext: "a task", DiffPatch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n",
		Cwd: t.TempDir(), Deadline: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("critic round: %v", err)
	}
	if !strings.Contains(res.Markdown, "no findings") {
		t.Fatalf("critic answered %q, want the stub's reply", res.Markdown)
	}
	var chats int
	for _, c := range w.coreCalls() {
		if c.bearer != "pat_k1.value" {
			t.Errorf("%s presented %q, want the model key", c.path, c.bearer)
		}
		if c.path == "/v1/models/openai/v1/chat/completions" {
			chats++
			if c.model != "anthropic/claude-sonnet-4.6" || !c.stream {
				t.Errorf("chat call = %+v, want a stream of anthropic/claude-sonnet-4.6", c)
			}
		}
	}
	if chats != 1 {
		t.Fatalf("chat calls = %d, want 1: %+v", chats, w.coreCalls())
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
