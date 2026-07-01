// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"latere.ai/x/topos"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models"

	"golang.org/x/term"
)

// modelNamer is implemented by model adapters that can report the model id they
// request (the Anthropic adapter does), so the REPL can show it.
type modelNamer interface{ Model() string }

// modelString returns the model id a brain requests, or "unknown".
func modelString(m models.Model) string {
	if mn, ok := m.(modelNamer); ok {
		return mn.Model()
	}
	return "unknown"
}

// quietSDKLogs silences the SDK's INFO chatter (e.g. "loop: turn completed")
// that otherwise interleaves with the chat. The loop logs through slog.Default;
// warnings and errors are kept. Genuine failures still surface as returned
// errors and through the Observer.
func quietSDKLogs() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
}

// localSystemPrompt is the default instruction for `latere topos --local`: a
// coding assistant working directly in the user's directory.
const localSystemPrompt = `You are a coding assistant running on the user's local machine, in their working directory.
Use the tools to read, search, edit, and run commands against their real files. Prefer read_file/edit_file/write_file for file changes and bash for commands. Be concise.`

// runToposLocal runs the agent loop entirely on this machine, like Claude Code:
// the model is your local Claude credential, the workspace is your directory, and
// tools execute directly on your files. No control plane, no Cella, no login.
// With oneShot set it runs a single prompt and exits; otherwise it is a REPL.
func runToposLocal(ctx context.Context, dir, modelName, oneShot string) error {
	quietSDKLogs()
	brain, err := buildLocalModel(ctx, modelName)
	if errors.Is(err, errNeedAuth) {
		// No credential yet. In an interactive terminal, show the provider picker
		// and retry; otherwise (one-shot -p, or piped) fail with a clear message
		// rather than launching a TUI with no TTY.
		if oneShot != "" || !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("no model credential — run `latere topos login`, or set ANTHROPIC_API_KEY")
		}
		if perr := runAuthPicker(ctx); perr != nil {
			return perr
		}
		brain, err = buildLocalModel(ctx, modelName)
	}
	if err != nil {
		return err
	}
	sb, err := newHostSandbox(dir)
	if err != nil {
		return err
	}
	const sandboxID = "local"

	runner, err := topos.NewRunner(topos.Options{
		Brain:    brain,
		Sandbox:  sb,
		Observer: renderLocalEvent,
	})
	if err != nil {
		return err
	}

	builtins := tools.Builtins()
	var transcript []models.Message
	turn := func(prompt string) error {
		res, terr := runner.Turn(ctx, topos.TurnInput{
			Sandbox:           sb,
			SandboxID:         sandboxID,
			SystemPrompt:      localSystemPrompt,
			Tools:             builtins,
			InitialTranscript: transcript,
			UserPrompt:        prompt,
		})
		if terr != nil {
			return terr
		}
		transcript = res.Transcript
		fmt.Println()
		return nil
	}

	if oneShot != "" {
		return turn(oneShot)
	}

	abs, _ := os.Getwd()
	fmt.Printf("Topos (local) in %s\n", abs)
	fmt.Printf("model: %s   ·   /model to switch, /help for commands, Ctrl+D to quit\n", modelString(brain))
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print("\n› ")
		if !sc.Scan() {
			fmt.Println()
			return nil
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := turn(line); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

// renderLocalEvent streams the run to the terminal: assistant text token by
// token, and a one-line marker per tool call.
func renderLocalEvent(e topos.Event) {
	switch e.Name {
	case topos.EventTextDelta:
		var p textDeltaPayload
		if json.Unmarshal(e.PayloadJSON, &p) == nil {
			fmt.Print(p.Text)
		}
	case "PreToolUse":
		var p preToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) == nil {
			fmt.Printf("\n● %s\n", p.ToolCall.Name)
		}
	case topos.EventPostToolUse:
		var p postToolUsePayload
		if json.Unmarshal(e.PayloadJSON, &p) == nil {
			mark := "ok"
			if p.Result.IsError {
				mark = "error"
			}
			fmt.Printf("  %s [%s] %s\n", p.ToolCall.Name, mark, truncLine(p.Result.Content, 100))
		}
	}
}
