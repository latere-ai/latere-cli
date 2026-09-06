// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"

	"github.com/latere-ai/latere-cli/internal/api"
)

// Run the actual Bubble Tea event loop over local HTTP/WebSocket connections,
// with terminal rendering disabled so no platform-specific PTY is needed.
func TestInteractiveSessionConnectionOutcomeE2E(t *testing.T) {
	oldBackoff, oldTries := reconnectBackoff, maxReconnectTries
	reconnectBackoff, maxReconnectTries = time.Millisecond, 2
	defer func() { reconnectBackoff, maxReconnectTries = oldBackoff, oldTries }()
	for _, tc := range []struct {
		name      string
		status    int
		dropFirst bool
		recover   bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "missing session", status: http.StatusNotFound},
		{name: "server failure", status: http.StatusServiceUnavailable},
		{name: "reconnect exhausted", status: http.StatusServiceUnavailable, dropFirst: true},
		{name: "recovered", status: http.StatusServiceUnavailable, recover: true},
		{name: "normal close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				drop := tc.dropFirst && n == 1
				if tc.status != 0 && !drop && (!tc.recover || n == 1) {
					w.WriteHeader(tc.status)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if drop {
					return
				}
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"status","state":"closed"}`)); err != nil {
					t.Error(err)
					return
				}
				_, _, _ = conn.Read(ctx) // Hold open until the client exits normally.
			}))
			defer server.Close()
			client := &api.Client{BaseURL: server.URL, Token: "test-token"}
			err := runInteractiveSession(ctx, client, "sess_test", false,
				tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
			wantCalls := int32(1)
			switch {
			case tc.recover:
				wantCalls = 2
			case tc.status != 0:
				wantCalls = 3
				if tc.dropFirst {
					wantCalls++
				}
			}
			if calls.Load() != wantCalls {
				t.Errorf("attach attempts = %d, want %d", calls.Load(), wantCalls)
			}
			if tc.status != 0 && !tc.recover {
				if err == nil || !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
					t.Errorf("connection failure returned %v, want HTTP %d diagnostic", err, tc.status)
				}
			} else if err != nil {
				t.Errorf("successful session returned an error: %v", err)
			}
		})
	}
}
