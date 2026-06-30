// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"latere.ai/x/topos"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models/fake"
	"latere.ai/x/topos/sandbox"
)

func TestHostSandboxFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sb, err := newHostSandbox(dir)
	if err != nil {
		t.Fatalf("newHostSandbox: %v", err)
	}
	ctx := context.Background()

	// Relative path resolves against the working directory.
	if err := sb.WriteFile(ctx, "local", "sub/note.txt", []byte("hi")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "sub/note.txt")); string(got) != "hi" {
		t.Fatalf("file on disk = %q, want hi", got)
	}
	data, err := sb.ReadFile(ctx, "local", "sub/note.txt")
	if err != nil || string(data) != "hi" {
		t.Fatalf("ReadFile = (%q, %v)", data, err)
	}

	// Absolute path is used as-is.
	abs := filepath.Join(dir, "abs.txt")
	if err := sb.WriteFile(ctx, "local", abs, []byte("x")); err != nil {
		t.Fatalf("WriteFile abs: %v", err)
	}
	if _, err := sb.ReadFile(ctx, "local", abs); err != nil {
		t.Fatalf("ReadFile abs: %v", err)
	}

	infos, err := sb.ListFiles(ctx, "local", ".")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	var names []string
	for _, fi := range infos {
		names = append(names, fi.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "abs.txt") {
		t.Fatalf("ListFiles = %v, want abs.txt", names)
	}
}

func TestHostSandboxExec(t *testing.T) {
	dir := t.TempDir()
	sb, _ := newHostSandbox(dir)
	ctx := context.Background()

	res, err := sb.Exec(ctx, "local", sandbox.ExecOptions{Argv: []string{"sh", "-lc", "echo hello-host && pwd"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "hello-host") {
		t.Fatalf("stdout = %q, want hello-host", res.Stdout)
	}
	// Commands run in the working directory.
	if !strings.Contains(string(res.Stdout), filepath.Base(dir)) {
		t.Fatalf("pwd output %q should be under the work dir %q", res.Stdout, dir)
	}
	// A non-zero exit is a result, not a Go error.
	res, err = sb.Exec(ctx, "local", sandbox.ExecOptions{Argv: []string{"sh", "-lc", "exit 3"}})
	if err != nil {
		t.Fatalf("non-zero exit should not be a Go error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
}

func TestBuildLocalModelCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_AUTO", "")
	if _, err := buildLocalModel(""); err == nil {
		t.Fatal("expected an error with no credentials")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	if m, err := buildLocalModel(""); err != nil || m == nil {
		t.Fatalf("buildLocalModel with API key = (%v, %v)", m, err)
	}
}

// TestLocalLoopEndToEnd drives the whole local stack with the fake model: the
// model emits a bash tool call, which the host sandbox executes against a real
// temp dir. This proves --local's loop + host provider + tools work end to end
// (only the real Claude call is swapped for the fake).
func TestLocalLoopEndToEnd(t *testing.T) {
	dir := t.TempDir()
	sb, err := newHostSandbox(dir)
	if err != nil {
		t.Fatalf("newHostSandbox: %v", err)
	}

	var toolOutput strings.Builder
	runner, err := topos.NewRunner(topos.Options{
		Brain:   fake.New(),
		Sandbox: sb,
		Observer: func(e topos.Event) {
			if e.Name == topos.EventPostToolUse {
				var p postToolUsePayload
				if json.Unmarshal(e.PayloadJSON, &p) == nil {
					toolOutput.WriteString(p.Result.Content)
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	res, err := runner.Turn(context.Background(), topos.TurnInput{
		Sandbox:      sb,
		SandboxID:    "local",
		SystemPrompt: localSystemPrompt,
		Tools:        tools.Builtins(),
		UserPrompt:   "marker-12345",
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(res.Transcript) == 0 {
		t.Fatal("expected a non-empty transcript")
	}
	// The fake model echoes the prompt via bash; the host sandbox really ran it.
	if !strings.Contains(toolOutput.String(), "marker-12345") {
		t.Fatalf("bash tool output %q should contain the echoed prompt (host exec ran)", toolOutput.String())
	}
}
