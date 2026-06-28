// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bytes"
	"context"
	"errors"
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

	// A protocol error frame is printed but does not end the stream.
	out.Reset()
	errOut.Reset()
	if done, _ := handlePrintFrame(attachFrame{Type: "error", Message: "nope"}, &out, &errOut); done {
		t.Fatal("error frame should not end the stream")
	}
	if !strings.Contains(errOut.String(), "nope") {
		t.Fatalf("stderr = %q, want the error message", errOut.String())
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

func TestStreamPrintReturnsRunError(t *testing.T) {
	fc := &fakePrintConn{frames: make(chan attachFrame, 4)}
	fc.frames <- ev("RunError", `{"error":"offline"}`)
	var out, errOut bytes.Buffer
	err := streamPrint(context.Background(), fc, &out, &errOut, "")
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err = %v, want offline", err)
	}
	var pe *printErr
	if !errors.As(err, &pe) {
		t.Fatalf("err type = %T, want *printErr", err)
	}
}

func TestStreamPrintStopsWhenChannelCloses(t *testing.T) {
	fc := &fakePrintConn{frames: make(chan attachFrame)}
	close(fc.frames)
	var out, errOut bytes.Buffer
	if err := streamPrint(context.Background(), fc, &out, &errOut, ""); err != nil {
		t.Fatalf("closed channel should end cleanly, got %v", err)
	}
}

func TestStreamPrintContextCancel(t *testing.T) {
	fc := &fakePrintConn{frames: make(chan attachFrame)} // never sends
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if err := streamPrint(ctx, fc, &out, &errOut, ""); err == nil {
		t.Fatal("want context error")
	}
}
