// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// printErr is returned by the print-mode stream when the agent reported an
// infrastructure error, so the command can exit non-zero for scripts/CI.
type printErr struct{ msg string }

func (e *printErr) Error() string { return e.msg }

// handlePrintFrame renders one frame for non-interactive (print) mode. It writes
// human output to out and tool/diagnostic lines to errOut, and reports whether
// the turn is complete (done) and any agent error. It is pure over its writers,
// so it is unit-testable without a WebSocket.
//
// Print mode shows the assembled assistant message (not token deltas) so piped
// output is clean, plus a one-line summary per tool call. A turn ends on the
// Stop event; a RunError ends it with an error; a closed session ends it too.
func handlePrintFrame(fr attachFrame, out, errOut io.Writer) (done bool, err error) {
	switch fr.Type {
	case "status":
		if fr.State == "closed" {
			return true, nil
		}
		return false, nil
	case "error":
		// A protocol/auth error frame (distinct from an agent RunError).
		fprintln(errOut, "error:", fr.Message)
		return false, nil
	case "event":
		return handlePrintEvent(fr, out, errOut)
	default:
		return false, nil
	}
}

func handlePrintEvent(fr attachFrame, out, errOut io.Writer) (bool, error) {
	switch fr.Event {
	case "AssistantMessage":
		var p assistantMessagePayload
		if json.Unmarshal(fr.Payload, &p) == nil && p.Text != "" {
			fprintln(out, p.Text)
		}
	case "PostToolUse":
		var p postToolUsePayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			status := "ok"
			if p.Result.IsError {
				status = "error"
			}
			fmt.Fprintf(errOut, "· %s [%s]\n", p.ToolCall.Name, status) //nolint:errcheck
		}
	case "PostToolUseFailure":
		var p postToolUseFailurePayload
		if json.Unmarshal(fr.Payload, &p) == nil {
			fmt.Fprintf(errOut, "· %s [denied/failed]\n", p.ToolCall.Name) //nolint:errcheck
		}
	case "RunError":
		var p runErrorPayload
		_ = json.Unmarshal(fr.Payload, &p)
		return true, &printErr{msg: p.Error}
	case "Stop":
		// The turn completed.
		return true, nil
	}
	return false, nil
}

// printConn is the subset of attachConn streamPrint needs (so it is testable
// with a fake).
type printConn interface {
	Frames() <-chan attachFrame
	Send(ctx context.Context, ctrl attachControl) error
}

// streamPrint runs a session in non-interactive print mode: it optionally
// submits a turn, then streams frames to the writers until the turn completes,
// the session closes, or the context ends. Returns a non-nil error if the agent
// reported a RunError.
func streamPrint(ctx context.Context, conn printConn, out, errOut io.Writer, turn string) error {
	if turn != "" {
		if err := conn.Send(ctx, attachControl{Type: "user_turn", Text: turn}); err != nil {
			return fmt.Errorf("send turn: %w", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case fr, ok := <-conn.Frames():
			if !ok {
				return nil // connection closed
			}
			done, err := handlePrintFrame(fr, out, errOut)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}
