// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestInteractiveSessionDisconnectBackoffE2E(t *testing.T) {
	oldBackoff := reconnectBackoff
	reconnectBackoff = 25 * time.Millisecond
	defer func() { reconnectBackoff = oldBackoff }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	times := make(chan time.Time, 4)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n > 4 {
			t.Error("unexpected extra connection")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		times <- time.Now()
		if since := r.URL.Query().Get("since"); since != strconv.Itoa(int(n-1)) {
			t.Errorf("reconnect %d: since=%q, want %d", n, since, n-1)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		frame := fmt.Sprintf(`{"type":"event","event":"AssistantMessage","seq":%d,"payload":{"text":"progress"}}`, n)
		if n == 4 {
			frame = `{"type":"status","state":"closed"}`
		}
		if err := conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			t.Error(err)
			return
		}
		if n == 4 {
			_, _, _ = conn.Read(ctx) // Let the client close the final connection.
		}
	}))
	defer server.Close()
	client := &api.Client{BaseURL: server.URL, Token: "test-token"}
	if err := runInteractiveSession(ctx, client, "sess_test", true,
		tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler()); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts = %d, want 4", attempts.Load())
	}
	previous := <-times
	for range 3 {
		current := <-times
		if delay := current.Sub(previous); delay < reconnectBackoff {
			t.Errorf("server received reconnect after %v, want at least %v", delay, reconnectBackoff)
		}
		previous = current
	}
}
