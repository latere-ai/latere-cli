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

// printErr reports a session failure or required intervention in print mode,
// so the command exits non-zero for scripts/CI.
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
		message := fr.Message
		if message == "" {
			message = "session protocol error"
		}
		return true, &printErr{msg: message}
	case "event":
		return handlePrintEvent(fr, out, errOut)
	default:
		return false, nil
	}
}

func handlePrintEvent(fr attachFrame, out, errOut io.Writer) (bool, error) {
	switch fr.Event {
	case "BudgetBreach":
		message, err := budgetBreachMessage(fr.Payload)
		if err != nil {
			return false, err
		}
		return true, &printErr{msg: message}
	case "AssistantMessage":
		var p assistantMessagePayload
		if err := json.Unmarshal(fr.Payload, &p); err != nil {
			return false, fmt.Errorf("decode %s payload: %w", fr.Event, err)
		}
		if p.Text != "" {
			if _, err := fmt.Fprintln(out, p.Text); err != nil {
				return false, fmt.Errorf("write assistant output: %w", err)
			}
		}
	case "PostToolUse":
		var p postToolUsePayload
		if err := json.Unmarshal(fr.Payload, &p); err != nil {
			return false, fmt.Errorf("decode %s payload: %w", fr.Event, err)
		}
		status := "ok"
		if p.Result.IsError {
			status = "error"
		}
		if _, err := fmt.Fprintf(errOut, "· %s [%s]\n", p.ToolCall.Name, status); err != nil {
			return false, fmt.Errorf("write tool output: %w", err)
		}
	case "PostToolUseFailure":
		var p postToolUseFailurePayload
		if err := json.Unmarshal(fr.Payload, &p); err != nil {
			return false, fmt.Errorf("decode %s payload: %w", fr.Event, err)
		}
		if _, err := fmt.Fprintf(errOut, "· %s [denied/failed]\n", p.ToolCall.Name); err != nil {
			return false, fmt.Errorf("write tool output: %w", err)
		}
	case "RunError":
		var p runErrorPayload
		if err := json.Unmarshal(fr.Payload, &p); err != nil {
			return false, fmt.Errorf("decode %s payload: %w", fr.Event, err)
		}
		if p.Error == "" {
			p.Error = "agent reported an error"
		}
		return true, &printErr{msg: p.Error}
	case "ApprovalRequest":
		var p approvalRequestPayload
		if err := json.Unmarshal(fr.Payload, &p); err != nil {
			return false, fmt.Errorf("decode %s payload: %w", fr.Event, err)
		}
		message := "approval required"
		if p.ToolID != "" {
			message += fmt.Sprintf(" for %q", p.ToolID)
		}
		return true, &printErr{msg: message + "; attach without --print to review the request"}
	case "Stop":
		var p stopPayload
		if len(fr.Payload) != 0 {
			if err := json.Unmarshal(fr.Payload, &p); err != nil {
				return false, fmt.Errorf("decode Stop payload: %w", err)
			}
		}
		if message := stopFailureMessage(p.StopReason); message != "" {
			return true, &printErr{msg: message}
		}
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
// reported an error, output could not be written, or the connection ended
// before completion was confirmed.
func streamPrint(ctx context.Context, conn printConn, out, errOut io.Writer, turn string) error {
	replaying := turn != ""
	if replaying {
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
				if err := ctx.Err(); err != nil {
					return err
				}
				return fmt.Errorf("session disconnected before turn completed: %w", io.ErrUnexpectedEOF)
			}
			// The server replays history before reading new input. A follow-up
			// prompt must not render or finish on an earlier turn's events.
			// With no new prompt, replay may contain the result of session start.
			if replaying {
				switch fr.Type {
				case "caught_up":
					replaying = false
					continue
				case "event":
					continue
				}
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
