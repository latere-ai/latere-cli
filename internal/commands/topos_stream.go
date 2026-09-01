// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"errors"
	"sync"
	"time"

	"latere.ai/x/pkg/wait"
)

// errNotConnected is returned by frameStream.Send when no connection is live.
var errNotConnected = errors.New("not connected")

// reconnect tuning (vars so tests can shrink them).
var (
	reconnectBackoff  = time.Second
	maxReconnectTries = 5
)

// streamMsg is one item delivered by a frameStream: either a frame, or a
// connection note ("connected", "reconnecting", "closed").
type streamMsg struct {
	frame *attachFrame
	note  string
}

// frameStream maintains an attach connection, delivering frames to a single
// channel and transparently reconnecting (from the last seen seq) when the
// connection drops — so the UI never sees a gap. It is the I/O layer beneath the
// TUI; the model only reads Events() and calls Send().
type frameStream struct {
	events chan streamMsg
	ctx    context.Context
	cancel context.CancelFunc
	dial   func(ctx context.Context, since int64) (*attachConn, error)

	mu   sync.Mutex
	conn *attachConn
}

// newFrameStream starts a stream that dials via dial(ctx, since). The first dial
// happens in the background goroutine, so newFrameStream never blocks.
func newFrameStream(ctx context.Context, dial func(ctx context.Context, since int64) (*attachConn, error)) *frameStream {
	sctx, cancel := context.WithCancel(ctx)
	fs := &frameStream{
		events: make(chan streamMsg, 256),
		ctx:    sctx,
		cancel: cancel,
		dial:   dial,
	}
	go fs.run()
	return fs
}

// Events returns the channel of stream messages; it closes when the stream ends.
func (fs *frameStream) Events() <-chan streamMsg { return fs.events }

// Send writes a control message to the current connection.
func (fs *frameStream) Send(ctrl attachControl) error {
	fs.mu.Lock()
	conn := fs.conn
	fs.mu.Unlock()
	if conn == nil {
		return errNotConnected
	}
	return conn.Send(fs.ctx, ctrl)
}

// Close stops the stream and its connection.
func (fs *frameStream) Close() { fs.cancel() }

func (fs *frameStream) run() {
	defer close(fs.events)
	var since int64
	tries := 0
	for {
		conn, err := fs.dial(fs.ctx, since)
		if err != nil {
			tries++
			if fs.ctx.Err() != nil || tries > maxReconnectTries {
				fs.emit(streamMsg{note: "closed"})
				return
			}
			fs.emit(streamMsg{note: "reconnecting"})
			if wait.Sleep(fs.ctx, reconnectBackoff) != nil {
				return
			}
			continue
		}
		tries = 0
		fs.setConn(conn)
		fs.emit(streamMsg{note: "connected"})
		since = fs.pump(conn, since)
		fs.setConn(nil)
		conn.Close()
		if fs.ctx.Err() != nil {
			fs.emit(streamMsg{note: "closed"})
			return
		}
		// The connection dropped without us stopping: reconnect from the cursor.
		fs.emit(streamMsg{note: "reconnecting"})
	}
}

// pump forwards frames until the connection closes, tracking the durable cursor.
func (fs *frameStream) pump(conn *attachConn, since int64) int64 {
	for {
		select {
		case <-fs.ctx.Done():
			return since
		case fr, ok := <-conn.Frames():
			if !ok {
				return since
			}
			if fr.Seq > since && !fr.Ephemeral {
				since = fr.Seq
			}
			f := fr
			fs.emit(streamMsg{frame: &f})
		}
	}
}

func (fs *frameStream) setConn(c *attachConn) {
	fs.mu.Lock()
	fs.conn = c
	fs.mu.Unlock()
}

// emit delivers a message unless the stream is shutting down.
func (fs *frameStream) emit(m streamMsg) {
	select {
	case fs.events <- m:
	case <-fs.ctx.Done():
	}
}
