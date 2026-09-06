// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestAttachReportsMalformedFrames(t *testing.T) {
	for _, tc := range []struct {
		name, raw, wantType string
	}{
		{"syntax", `{`, "error"},
		{"array", `[]`, "error"},
		{"null", `null`, "error"},
		{"empty object", `{}`, "error"},
		{"wrong type", `{"type":42}`, "error"},
		{"wrong sequence", `{"type":"event","seq":"bad"}`, "error"},
		{"trailing JSON", `{"type":"status"} {}`, "error"},
		{"extension field", `{"type":"status","state":"running","new_field":true}`, "status"},
		{"future type", `{"type":"future_frame"}`, "future_frame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				for _, raw := range []string{tc.raw, `{"type":"event","event":"Stop","seq":9}`} {
					if err := conn.Write(ctx, websocket.MessageText, []byte(raw)); err != nil {
						t.Error(err)
						return
					}
				}
				_ = conn.Close(websocket.StatusNormalClosure, "done")
			}))
			defer server.Close()
			conn, err := dialAttach(ctx, server.URL, "test-token", "sess_test", 0, false)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var frames []attachFrame
			for frame := range conn.Frames() {
				frames = append(frames, frame)
			}
			if len(frames) != 2 || frames[0].Type != tc.wantType || frames[1].Event != "Stop" {
				t.Fatalf("frames = %+v, want %s followed by Stop", frames, tc.wantType)
			}
			if tc.wantType == "error" {
				if !strings.Contains(frames[0].Message, "decode session frame") || frames[0].Seq != 0 {
					t.Fatalf("invalid decode error: %+v", frames[0])
				}
				model := newTUIModel("sess_test", nil, &recSender{}, false)
				updated, _ := model.Update(streamEventMsg{ok: true, m: streamMsg{frame: &frames[0]}})
				if !strings.Contains(updated.View(), "decode session frame") {
					t.Errorf("interactive view hid the decode error: %s", updated.View())
				}
			}
		})
	}
}
