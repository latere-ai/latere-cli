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
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"
)

// attachFrame is one server→client message over the Topos attach WebSocket. It
// mirrors the control plane's wire frame. Type discriminates: "event" wraps a
// session event (Event is the hook name), "caught_up" marks the end of replay,
// "status" reports a lifecycle change, and "error" reports a problem.
type attachFrame struct {
	Type      string          `json:"type"`
	Seq       int64           `json:"seq"`
	Event     string          `json:"event"`
	Payload   json.RawMessage `json:"payload"`
	Ephemeral bool            `json:"ephemeral"`
	State     string          `json:"state"`
	Message   string          `json:"message"`
}

// attachControl is one client→server message: a user turn, an interrupt, or a
// reply to an approval request.
type attachControl struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	DecisionID string `json:"decision_id,omitempty"`
	Approve    bool   `json:"approve,omitempty"`
}

// --- payloads for the events the client renders ---

type assistantMessagePayload struct {
	Text string `json:"text"`
	Turn int    `json:"turn"`
}

type textDeltaPayload struct {
	Text string `json:"text"`
	Turn int    `json:"turn"`
}

type toolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

type postToolUsePayload struct {
	ToolCall toolCall   `json:"tool_call"`
	Result   toolResult `json:"result"`
}

type preToolUsePayload struct {
	ToolCall toolCall `json:"tool_call"`
}

type postToolUseFailurePayload struct {
	ToolCall toolCall `json:"tool_call"`
	Error    string   `json:"error"`
}

type approvalRequestPayload struct {
	DecisionID string          `json:"decision_id"`
	ToolID     string          `json:"tool_id"`
	Args       json.RawMessage `json:"args"`
}

type runErrorPayload struct {
	Error string `json:"error"`
}

type usageTotals struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type usagePayload struct {
	Total usageTotals `json:"total"`
}

type subagentPayload struct {
	SubagentID string `json:"subagent_id"`
}

// attachConn is a live attach connection: it reads frames into a channel and
// sends control messages. The frames channel closes when the connection ends.
type attachConn struct {
	ws     *websocket.Conn
	frames chan attachFrame
	cancel context.CancelFunc
}

// wsURLFromBase converts an http(s) base URL into the ws(s) attach URL for a
// session, with the given cursor and mode.
func wsURLFromBase(baseURL, sessionID string, since int64, readonly bool) string {
	u := "ws" + strings.TrimPrefix(strings.TrimRight(baseURL, "/"), "http")
	q := "?since=" + strconv.FormatInt(since, 10)
	if readonly {
		q += "&mode=ro"
	}
	return u + "/v1/sessions/" + sessionID + "/attach" + q
}

// dialAttach opens an attach WebSocket and starts reading frames. The bearer is
// sent as the Authorization header (Topos validates it like any /v1 request).
func dialAttach(ctx context.Context, baseURL, token, sessionID string, since int64, readonly bool) (*attachConn, error) {
	dctx, cancel := context.WithCancel(ctx)
	opts := &websocket.DialOptions{}
	if token != "" {
		opts.HTTPHeader = http.Header{"Authorization": []string{"Bearer " + token}}
	}
	// The handshake response is never the caller's to close. websocket.Dial
	// leaves it nil on success, where the connection owns the underlying
	// socket and closing would tear down the upgraded stream, and has already
	// closed it on failure.
	ws, _, err := websocket.Dial(dctx, wsURLFromBase(baseURL, sessionID, since, readonly), opts) //nolint:bodyclose
	if err != nil {
		cancel()
		return nil, fmt.Errorf("attach: %w", err)
	}
	// Large reads: a turn's assembled assistant message can be sizeable.
	ws.SetReadLimit(8 << 20)
	a := &attachConn{ws: ws, frames: make(chan attachFrame, 256), cancel: cancel}
	go a.readLoop(dctx)
	return a, nil
}

// readLoop reads frames until the connection ends, then closes the channel.
func (a *attachConn) readLoop(ctx context.Context) {
	defer close(a.frames)
	for {
		_, data, err := a.ws.Read(ctx)
		if err != nil {
			return
		}
		var fr attachFrame
		if err := json.Unmarshal(data, &fr); err != nil {
			continue // skip a malformed frame rather than tearing down
		}
		select {
		case a.frames <- fr:
		case <-ctx.Done():
			return
		}
	}
}

// Frames returns the receive channel of incoming frames; it closes on disconnect.
func (a *attachConn) Frames() <-chan attachFrame { return a.frames }

// Send writes a control message to the server.
func (a *attachConn) Send(ctx context.Context, ctrl attachControl) error {
	b, err := json.Marshal(ctrl)
	if err != nil {
		return err
	}
	return a.ws.Write(ctx, websocket.MessageText, b)
}

// Close tears down the connection.
func (a *attachConn) Close() {
	a.cancel()
	if a.ws != nil {
		_ = a.ws.Close(websocket.StatusNormalClosure, "")
	}
}
