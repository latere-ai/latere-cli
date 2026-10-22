// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"latere.ai/x/agon"
	"latere.ai/x/agon/critic"
	"latere.ai/x/agon/input"
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
		{"model", "anthropic/claude-sonnet-5.5"},
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
// sends its model call to the core's Lux door with the model key and the
// catalog name of the default critic model, once the core accepted the key,
// and reports the call's usage.
func TestReviewCriticCallsTheDoor(t *testing.T) {
	w := newKeyWorld(t, "")
	t.Setenv("AUTH_URL", w.srv.URL)
	key, err := modelKeySource(w.modelsURL(), "")(t.Context())
	if err != nil {
		t.Fatalf("key source: %v", err)
	}
	model, err := reviewCriticModel(w.modelsURL(), key)
	if err != nil {
		t.Fatal(err)
	}
	critics := critic.NewCriticFactory(critic.Config{Model: model, Name: defaultCatalogModel})
	res, err := critics(1).Round(t.Context(), agon.CriticInput{
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
	if res.Tokens != 12 {
		t.Errorf("critic reported %d tokens, want the stub's 12", res.Tokens)
	}
	var generates int
	for _, c := range w.coreCalls() {
		if c.bearer != "pat_k1.value" {
			t.Errorf("%s presented %q, want the model key", c.path, c.bearer)
		}
		if c.path == "/v1/models/lux/v1/generate" {
			generates++
			if c.model != "anthropic/claude-sonnet-5.5" || c.stream {
				t.Errorf("generate call = %+v, want one answer of anthropic/claude-sonnet-5.5", c)
			}
		}
	}
	if generates != 1 {
		t.Fatalf("generate calls = %d, want 1: %+v", generates, w.coreCalls())
	}
}

// A models URL that is not served under /v1/models names no origin the
// critics' client can build its paths on, and is refused before a round.
func TestReviewCriticNeedsTheModelsPath(t *testing.T) {
	if _, err := reviewCriticModel("https://models.example/other", "pat_k1.value"); err == nil || !strings.Contains(err.Error(), "/v1/models") {
		t.Fatalf("reviewCriticModel = %v, want a refusal naming /v1/models", err)
	}
	if origin, ok := modelsOrigin("https://api.example/v1/models/"); !ok || origin != "https://api.example" {
		t.Fatalf("modelsOrigin = %q, %v", origin, ok)
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
