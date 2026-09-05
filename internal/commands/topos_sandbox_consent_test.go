// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"latere.ai/x/topos/sandbox"
)

type promptEvents chan string

func (p promptEvents) Write(b []byte) (int, error) { p <- string(b); return len(b), nil }

func awaitConsent(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Errorf("consent = %v, want %v", err, want)
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("consent remained blocked after cancellation")
	}
}

func TestPromptExecConsentWholeAnswer(t *testing.T) {
	for _, input := range []string{"yes no\n", "y then run something else\n"} {
		decide := promptExecConsent(strings.NewReader(input), io.Discard)
		if err := decide(t.Context(), "sb", sandbox.ExecOptions{Argv: []string{"command"}}); err == nil {
			t.Errorf("malformed approval %q accepted", input)
		}
	}
}

func TestPromptExecConsentCancelAndDiscardLateAnswer(t *testing.T) {
	in, input := io.Pipe()
	defer in.Close()
	defer input.Close()
	prompts := make(promptEvents, 8)
	decide := promptExecConsent(in, prompts)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- decide(ctx, "one", sandbox.ExecOptions{Argv: []string{"first"}}) }()
	select {
	case <-prompts:
	case <-time.After(time.Second):
		t.Fatal("first prompt missing")
	}
	cancel()
	awaitConsent(t, first, context.Canceled)
	second := make(chan error, 1)
	go func() { second <- decide(t.Context(), "two", sandbox.ExecOptions{Argv: []string{"second"}}) }()
	select {
	case <-prompts:
		t.Error("second prompt overlapped the unanswered first prompt")
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := io.WriteString(input, "yes\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-prompts:
		if !strings.Contains(text, "second") {
			t.Errorf("unexpected prompt: %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("second prompt did not start after stale answer was discarded")
	}
	select {
	case err := <-second:
		t.Fatalf("stale answer completed the second request: %v", err)
	default:
	}
	if _, err := io.WriteString(input, "n\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("second request allowed despite denial")
		}
	case <-time.After(time.Second):
		t.Fatal("second answer was not processed")
	}
}

func TestPromptExecConsentCanceledWaitKeepsActivePrompt(t *testing.T) {
	in, input := io.Pipe()
	defer in.Close()
	defer input.Close()
	prompts := make(promptEvents, 8)
	decide := promptExecConsent(in, prompts)
	active := make(chan error, 1)
	go func() { active <- decide(t.Context(), "one", sandbox.ExecOptions{Argv: []string{"first"}}) }()
	select {
	case <-prompts:
	case <-time.After(time.Second):
		t.Fatal("active prompt missing")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	waiting := make(chan error, 1)
	go func() { waiting <- decide(ctx, "two", sandbox.ExecOptions{Argv: []string{"second"}}) }()
	<-ctx.Done()
	awaitConsent(t, waiting, context.DeadlineExceeded)
	select {
	case <-prompts:
		t.Error("waiting request printed another prompt")
	default:
	}
	if _, err := io.WriteString(input, "yes\n"); err != nil {
		t.Fatal(err)
	}
	awaitConsent(t, active, nil)
}

func TestPromptExecConsentShowsExactCommand(t *testing.T) {
	var out strings.Builder
	opts := sandbox.ExecOptions{
		Argv:      []string{"printf", "one two", "line\nbreak", "\x1b[2J"},
		Cwd:       "different directory",
		Env:       map[string]string{"MODE": "changed"},
		SecretEnv: map[string]string{"TOKEN": "secret-entry"},
	}
	decide := promptExecConsent(strings.NewReader("n\n"), &out)
	_ = decide(t.Context(), "sb", opts)
	for _, want := range []string{`"one two"`, `"line\nbreak"`, `"\x1b[2J"`, `"different directory"`, `"MODE"`, `"TOKEN"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt omits %s: %q", want, out.String())
		}
	}
	if strings.ContainsRune(out.String(), '\x1b') {
		t.Error("terminal control sequence was printed verbatim")
	}
	if strings.Contains(out.String(), "changed") || strings.Contains(out.String(), "secret-entry") {
		t.Error("environment values appeared in the prompt")
	}
}

type failingConsentIO struct{ err error }

func (f failingConsentIO) Read([]byte) (int, error)  { return 0, f.err }
func (f failingConsentIO) Write([]byte) (int, error) { return 0, f.err }

func TestPromptExecConsentIOAndReusableAnswers(t *testing.T) {
	sentinel := errors.New("broken terminal")
	for name, decide := range map[string]sandbox.ConsentFunc{
		"output": promptExecConsent(strings.NewReader("yes\n"), failingConsentIO{sentinel}),
		"input":  promptExecConsent(io.MultiReader(strings.NewReader("yes"), failingConsentIO{sentinel}), io.Discard),
	} {
		if err := decide(t.Context(), "sb", sandbox.ExecOptions{}); !errors.Is(err, sentinel) {
			t.Errorf("%s failure = %v", name, err)
		}
	}
	decide := promptExecConsent(strings.NewReader("  YES  \nno\nyes"), io.Discard)
	for i, allowed := range []bool{true, false, true, false} {
		if err := decide(t.Context(), "sb", sandbox.ExecOptions{}); (err == nil) != allowed {
			t.Errorf("answer %d = %v; allowed=%v", i, err, allowed)
		}
	}
}

func TestPromptExecConsentAlreadyCanceled(t *testing.T) {
	var output strings.Builder
	decide := promptExecConsent(strings.NewReader("yes\n"), &output)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := decide(ctx, "sb", sandbox.ExecOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("canceled request printed a prompt")
	}
	if err := decide(t.Context(), "sb", sandbox.ExecOptions{}); err != nil {
		t.Fatalf("canceled request consumed the answer: %v", err)
	}
}
