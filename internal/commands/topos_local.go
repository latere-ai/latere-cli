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
	"os"
	"strings"

	"latere.ai/x/topos"
	"latere.ai/x/topos/harness/tools"
	"latere.ai/x/topos/models"
	"latere.ai/x/topos/models/anthropic"
)

// localSystemPrompt is the default instruction for `latere topos --local`: a
// coding assistant working directly in the user's directory.
const localSystemPrompt = `You are a coding assistant running on the user's local machine, in their working directory.
Use the tools to read, search, edit, and run commands against their real files. Prefer read_file/edit_file/write_file for file changes and bash for commands. Be concise.`

// runToposLocal runs the agent loop entirely on this machine, like Claude Code:
// the model is your local Claude credential, the workspace is your directory, and
// tools execute directly on your files. No control plane, no Cella, no login.
// With oneShot set it runs a single prompt and exits; otherwise it is a REPL.
func runToposLocal(ctx context.Context, dir, modelName, oneShot string) error {
	brain, err := buildLocalModel(ctx, modelName)
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
	fmt.Printf("Topos (local) in %s — type a message, Ctrl+D to quit.\n", abs)
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

// buildLocalModel builds the Anthropic model from a local credential: an API key
// (ANTHROPIC_API_KEY) or a Claude Code OAuth token (CLAUDE_CODE_OAUTH_TOKEN).
func buildLocalModel(ctx context.Context, modelName string) (models.Model, error) {
	var opts []anthropic.Option
	if modelName != "" {
		opts = append(opts, anthropic.WithModel(modelName))
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return anthropic.New(key, "", opts...), nil
	}
	// CLAUDE_CODE_OAUTH_TOKEN and the _AUTO variant are the Claude Code OAuth
	// tokens (prefix sk-ant-oat); both go on the Authorization: Bearer header.
	for _, env := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_AUTO"} {
		if tok := os.Getenv(env); tok != "" {
			return anthropic.New(tok, "", append(opts, anthropic.WithOAuthToken())...), nil
		}
	}
	// Our own Claude OAuth login (latere topos login), refreshed as needed.
	if tok, err := claudeOAuthBearer(ctx); err != nil {
		return nil, err
	} else if tok != "" {
		return anthropic.New(tok, "", append(opts, anthropic.WithOAuthToken())...), nil
	}
	return nil, errors.New("no Claude credential — run `latere topos login`, or set ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN")
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
