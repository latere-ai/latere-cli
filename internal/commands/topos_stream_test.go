// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"errors"
	"testing"
	"time"
)

// drainUntil reads stream messages until pred is satisfied or the deadline hits.
func drainUntil(t *testing.T, fs *frameStream, pred func(streamMsg) bool) streamMsg {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m, ok := <-fs.Events():
			if !ok {
				t.Fatal("stream closed before predicate matched")
			}
			if pred(m) {
				return m
			}
		case <-deadline:
			t.Fatal("timed out waiting for stream message")
		}
	}
}

func TestFrameStreamReconnectsFromCursor(t *testing.T) {
	old := reconnectBackoff
	reconnectBackoff = time.Millisecond
	defer func() { reconnectBackoff = old }()

	var sinceArgs []int64
	dialN := 0
	dial := func(ctx context.Context, since int64) (*attachConn, error) {
		sinceArgs = append(sinceArgs, since)
		dialN++
		// Build an attachConn whose frames channel we drive directly.
		ch := make(chan attachFrame, 4)
		if dialN == 1 {
			ch <- attachFrame{Type: "event", Event: "AssistantMessage", Seq: 7}
			close(ch) // simulate a drop after one frame
		} else {
			ch <- attachFrame{Type: "event", Event: "Stop", Seq: 8}
			// leave open; the test cancels via fs.Close
		}
		return &attachConn{frames: ch, ws: nil, cancel: func() {}}, nil
	}

	fs := newFrameStream(context.Background(), dial)
	defer fs.Close()

	// First connection delivers seq 7, then drops and reconnects.
	drainUntil(t, fs, func(m streamMsg) bool { return m.frame != nil && m.frame.Seq == 7 })
	drainUntil(t, fs, func(m streamMsg) bool { return m.note == "reconnecting" })
	drainUntil(t, fs, func(m streamMsg) bool { return m.frame != nil && m.frame.Seq == 8 })

	if dialN < 2 {
		t.Fatalf("dialN = %d, want >= 2 (reconnected)", dialN)
	}
	// The reconnect dial must carry the cursor from the last durable frame (7).
	if len(sinceArgs) < 2 || sinceArgs[1] != 7 {
		t.Fatalf("reconnect since = %v, want second dial since=7", sinceArgs)
	}
}

func TestFrameStreamGivesUpAfterMaxTries(t *testing.T) {
	oldB, oldT := reconnectBackoff, maxReconnectTries
	reconnectBackoff = time.Millisecond
	maxReconnectTries = 2
	defer func() { reconnectBackoff, maxReconnectTries = oldB, oldT }()

	dialErr := errors.New("refused")
	dial := func(ctx context.Context, since int64) (*attachConn, error) {
		return nil, dialErr
	}
	fs := newFrameStream(context.Background(), dial)
	defer fs.Close()

	// Eventually a closed note arrives and the channel closes.
	got := drainUntil(t, fs, func(m streamMsg) bool { return m.note == "closed" })
	if got.note != "closed" {
		t.Fatalf("note = %q, want closed", got.note)
	}
	if !errors.Is(fs.Err(), dialErr) {
		t.Fatalf("terminal error = %v, want original dial error", fs.Err())
	}
}

func TestFrameStreamCloseDoesNotReportDialCancellation(t *testing.T) {
	started := make(chan struct{})
	fs := newFrameStream(t.Context(), func(ctx context.Context, since int64) (*attachConn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer fs.Close()
	<-started
	fs.Close()
	for range fs.Events() {
	}
	if err := fs.Err(); err != nil {
		t.Fatalf("user detach reported a failure: %v", err)
	}
}

func TestFrameStreamSendNotConnected(t *testing.T) {
	dial := func(ctx context.Context, since int64) (*attachConn, error) {
		return nil, errors.New("never")
	}
	fs := newFrameStream(context.Background(), dial)
	defer fs.Close()
	// Before any successful dial, Send reports not-connected.
	if err := fs.Send(attachControl{Type: "interrupt"}); !errors.Is(err, errNotConnected) {
		t.Fatalf("Send err = %v, want errNotConnected", err)
	}
}
