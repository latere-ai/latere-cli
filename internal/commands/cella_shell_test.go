// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// shell drives the attach socket: the request with the window goes first,
// the local input goes up, the output comes down, and the exit frame is the
// CLI's exit code.
func TestCellaShellAttach(t *testing.T) {
	f := newFakeCore(t)
	var (
		request socketRequest
		input   string
	)
	f.attach = func(conn *websocket.Conn) {
		kind, data, err := conn.ReadMessage()
		if err != nil || kind != websocket.TextMessage {
			t.Errorf("first frame: %v %v", kind, err)
			return
		}
		if err := json.Unmarshal(data, &request); err != nil {
			t.Errorf("request frame %q: %v", data, err)
			return
		}
		for !strings.Contains(input, "exit\n") {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				t.Errorf("read input: %v", err)
				return
			}
			if kind == websocket.BinaryMessage {
				input += string(data)
			}
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte("Python 3\n")); err != nil {
			t.Errorf("write output: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"exit":3}`)); err != nil {
			t.Errorf("write exit: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}
	out, _, err := f.runCella("print(1)\nexit\n", "shell", "dev", "--", "python3")
	if code := exitCode(err); code != 3 {
		t.Fatalf("exit code = %d (%v), want the shell's 3", code, err)
	}
	if out != "Python 3\n" {
		t.Errorf("output = %q", out)
	}
	if input != "print(1)\nexit\n" {
		t.Errorf("input = %q", input)
	}
	if !slices.Equal(request.Command, []string{"python3"}) || request.Cols != defaultTerminalCols || request.Rows != defaultTerminalRows {
		t.Errorf("request = %+v, want the command and the fixed window", request)
	}
	if got := f.seen(); !slices.Equal(got, []string{"GET /sandboxes/dev/attach"}) {
		t.Errorf("requests = %v", got)
	}
}

// An error frame ends the session with the control plane's refusal.
func TestCellaShellErrorFrame(t *testing.T) {
	f := newFakeCore(t)
	f.attach = func(conn *websocket.Conn) {
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Errorf("first frame: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":{"code":"phase_conflict","message":"The sandbox is not running."}}`))
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}
	_, _, err := f.runCella("", "attach", "dev")
	if err == nil || err.Error() != "The sandbox is not running." {
		t.Fatalf("err = %v, want the refusal", err)
	}
}
