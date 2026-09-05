// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestHandlePrintFrame(t *testing.T) {
	var out, errOut bytes.Buffer

	// AssistantMessage prints to stdout.
	done, err := handlePrintFrame(ev("AssistantMessage", `{"text":"the answer"}`), &out, &errOut)
	if done || err != nil {
		t.Fatalf("assistant: done=%v err=%v", done, err)
	}
	if !strings.Contains(out.String(), "the answer") {
		t.Fatalf("stdout = %q, want the assistant text", out.String())
	}

	// PostToolUse prints a summary to stderr.
	_, _ = handlePrintFrame(ev("PostToolUse", `{"tool_call":{"name":"bash"},"result":{"is_error":true}}`), &out, &errOut)
	if !strings.Contains(errOut.String(), "bash [error]") {
		t.Fatalf("stderr = %q, want a tool summary", errOut.String())
	}

	// PostToolUseFailure prints a denied/failed summary to stderr.
	errOut.Reset()
	_, _ = handlePrintFrame(ev("PostToolUseFailure", `{"tool_call":{"name":"bash"},"error":"denied"}`), &out, &errOut)
	if !strings.Contains(errOut.String(), "bash [denied/failed]") {
		t.Fatalf("stderr = %q, want a denied/failed summary", errOut.String())
	}

	// Stop ends the turn.
	if done, _ := handlePrintFrame(ev("Stop", `{}`), &out, &errOut); !done {
		t.Fatal("Stop should signal done")
	}

	// RunError ends the turn with an error.
	done, err = handlePrintFrame(ev("RunError", `{"error":"boom"}`), &out, &errOut)
	if !done || err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("RunError: done=%v err=%v", done, err)
	}

	// A closed status ends the stream.
	if done, _ := handlePrintFrame(attachFrame{Type: "status", State: "closed"}, &out, &errOut); !done {
		t.Fatal("closed status should signal done")
	}

	// A protocol error must fail the non-interactive command.
	out.Reset()
	errOut.Reset()
	done, err = handlePrintFrame(attachFrame{Type: "error", Message: "nope"}, &out, &errOut)
	if !done || err == nil || err.Error() != "nope" {
		t.Fatalf("protocol error: done=%v, err=%v", done, err)
	}
	if errOut.Len() != 0 {
		t.Fatal("returned protocol error must not also be printed")
	}
}

// fakePrintConn feeds canned frames and records sent control messages.
type fakePrintConn struct {
	frames chan attachFrame
	sent   []attachControl
}

func (f *fakePrintConn) Frames() <-chan attachFrame { return f.frames }
func (f *fakePrintConn) Send(_ context.Context, ctrl attachControl) error {
	f.sent = append(f.sent, ctrl)
	return nil
}

func TestStreamPrintSendsTurnAndStopsOnStop(t *testing.T) {
	fc := &fakePrintConn{frames: make(chan attachFrame, 8)}
	fc.frames <- attachFrame{Type: "caught_up"}
	fc.frames <- ev("AssistantMessage", `{"text":"hi"}`)
	fc.frames <- ev("Stop", `{}`)

	var out, errOut bytes.Buffer
	if err := streamPrint(context.Background(), fc, &out, &errOut, "do it"); err != nil {
		t.Fatalf("streamPrint: %v", err)
	}
	if len(fc.sent) != 1 || fc.sent[0].Type != "user_turn" || fc.sent[0].Text != "do it" {
		t.Fatalf("sent = %+v, want one user_turn 'do it'", fc.sent)
	}
	if !strings.Contains(out.String(), "hi") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestStreamPrintDistinguishesReplayFromNewTurn(t *testing.T) {
	for _, turn := range []string{"", "new prompt"} {
		t.Run(turn, func(t *testing.T) {
			fc := &fakePrintConn{frames: make(chan attachFrame, 8)}
			fc.frames <- ev("AssistantMessage", `{"text":"replayed answer"}`)
			fc.frames <- ev("Stop", `{}`)
			fc.frames <- ev("RunError", `{"error":"old failure"}`)
			fc.frames <- ev("PostToolUse", `{"tool_call":{"name":"old-tool"},"result":{}}`)
			fc.frames <- attachFrame{Type: "caught_up", Seq: 4}
			fc.frames <- ev("AssistantMessage", `{"text":"new answer"}`)
			fc.frames <- ev("Stop", `{}`)
			var out, errOut bytes.Buffer
			if err := streamPrint(t.Context(), fc, &out, &errOut, turn); err != nil {
				t.Fatal(err)
			}
			want, wantSent := "replayed answer\n", 0
			if turn != "" {
				want, wantSent = "new answer\n", 1
			}
			if out.String() != want || errOut.Len() != 0 || len(fc.sent) != wantSent {
				t.Errorf("output=%q, stderr=%q, sent=%d; want %q and %d sends", out.String(), errOut.String(), len(fc.sent), want, wantSent)
			}
		})
	}
}

func TestStreamPrintReturnsRunError(t *testing.T) {
	fc := &fakePrintConn{frames: make(chan attachFrame, 4)}
	fc.frames <- ev("RunError", `{"error":"offline"}`)
	var out, errOut bytes.Buffer
	err := streamPrint(context.Background(), fc, &out, &errOut, "")
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err = %v, want offline", err)
	}
	if _, ok := errors.AsType[*printErr](err); !ok {
		t.Fatalf("err type = %T, want *printErr", err)
	}
}

type failingPrintWriter struct{ err error }

func (w failingPrintWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStreamPrintPropagatesOutputErrors(t *testing.T) {
	for _, fr := range []attachFrame{
		ev("AssistantMessage", `{"text":"answer"}`),
		ev("PostToolUse", `{"tool_call":{"name":"test-tool"},"result":{}}`),
		ev("PostToolUseFailure", `{"tool_call":{"name":"test-tool"}}`),
	} {
		t.Run(fr.Event, func(t *testing.T) {
			fc := &fakePrintConn{frames: make(chan attachFrame, 2)}
			fc.frames <- fr
			fc.frames <- ev("Stop", `{}`)
			writeErr := errors.New("output storage full")
			writer := failingPrintWriter{err: writeErr}
			if err := streamPrint(t.Context(), fc, writer, writer, ""); !errors.Is(err, writeErr) {
				t.Fatalf("write failure lost: %v", err)
			}
			if len(fc.frames) != 1 {
				t.Fatal("stream consumed completion after losing output")
			}
		})
	}
}

func TestPrintRejectsMalformedEventPayloads(t *testing.T) {
	for _, event := range []string{"AssistantMessage", "PostToolUse", "PostToolUseFailure", "RunError", "ApprovalRequest"} {
		for _, payload := range []string{"", "{", "[]"} {
			t.Run(event+"/"+payload, func(t *testing.T) {
				var out, errOut bytes.Buffer
				_, err := handlePrintFrame(ev(event, payload), &out, &errOut)
				if err == nil || !strings.Contains(err.Error(), "decode "+event+" payload") {
					t.Fatalf("malformed payload not reported: %v", err)
				}
				_, syntaxError := errors.AsType[*json.SyntaxError](err)
				_, typeError := errors.AsType[*json.UnmarshalTypeError](err)
				if !syntaxError && !typeError {
					t.Fatalf("original decode error lost: %v", err)
				}
				if out.Len() != 0 || errOut.Len() != 0 {
					t.Fatal("invalid payload produced output")
				}
			})
		}
	}
}

func TestStreamPrintReportsApprovalWithoutDeciding(t *testing.T) {
	for _, payload := range []string{`{"decision_id":"d1","tool_id":"test-tool"}`, `{}`} {
		t.Run(payload, func(t *testing.T) {
			fc := &fakePrintConn{frames: make(chan attachFrame, 3)}
			fc.frames <- attachFrame{Type: "caught_up"}
			fc.frames <- ev("ApprovalRequest", payload)
			fc.frames <- ev("Stop", `{}`)
			var out, errOut bytes.Buffer
			err := streamPrint(t.Context(), fc, &out, &errOut, "new prompt")
			if err == nil || !strings.Contains(err.Error(), "approval required") || !strings.Contains(err.Error(), "attach without --print") {
				t.Fatalf("approval not reported: %v", err)
			}
			if len(fc.sent) != 1 || fc.sent[0].Type != "user_turn" {
				t.Fatalf("print mode sent an unsolicited approval reply: %v", fc.sent)
			}
			if out.Len() != 0 || errOut.Len() != 0 {
				t.Fatal("approval request produced output instead of a returned error")
			}
		})
	}
}

func TestStreamPrintFailsWhenChannelClosesBeforeCompletion(t *testing.T) {
	fc := &fakePrintConn{frames: make(chan attachFrame)}
	close(fc.frames)
	var out, errOut bytes.Buffer
	if err := streamPrint(context.Background(), fc, &out, &errOut, ""); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("closed channel must report incomplete turn, got %v", err)
	}
}

func TestStreamPrintContextCancel(t *testing.T) {
	for _, closed := range []bool{false, true} {
		fc := &fakePrintConn{frames: make(chan attachFrame)} // never sends
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if closed {
			close(fc.frames)
		}
		var out, errOut bytes.Buffer
		if err := streamPrint(ctx, fc, &out, &errOut, ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("closed=%t: want context cancellation, got %v", closed, err)
		}
	}
}
