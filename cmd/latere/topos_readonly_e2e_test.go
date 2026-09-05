// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestToposReadOnlyRejectsPromptBeforeAttachE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, auth := range []string{"signed in", "missing"} {
		for _, flag := range []string{"--print", "-p"} {
			for _, mode := range []string{"default", "--readonly", "--readonly=true", "--readonly=false"} {
				t.Run(auth+"/"+flag+"/"+mode, func(t *testing.T) {
					root := t.TempDir()
					readonly := mode == "--readonly" || mode == "--readonly=true"
					var requests, turns atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.URL.Path != "/v1/sessions/sess_test/attach" || r.URL.Query().Get("mode") != "" || r.Header.Get("Authorization") != "Bearer test-topos" {
							t.Error("unexpected attach request")
						}
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.CloseNow() }()
						_, data, err := conn.Read(r.Context())
						var turn struct {
							Type string `json:"type"`
							Text string `json:"text"`
						}
						if err != nil || json.Unmarshal(data, &turn) != nil || turn.Type != "user_turn" || turn.Text != "test prompt" {
							t.Errorf("invalid prompt: %v: %s", err, data)
						} else {
							turns.Add(1)
						}
						for _, frame := range []string{
							`{"type":"caught_up","seq":0}`,
							`{"type":"event","event":"AssistantMessage","seq":1,"payload":{"text":"test answer"}}`,
							`{"type":"event","event":"Stop","seq":2,"payload":{}}`,
						} {
							if err := conn.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
								t.Error(err)
								return
							}
						}
						_ = conn.Close(websocket.StatusNormalClosure, "done")
					}))
					defer server.Close()
					args := []string{"topos", "session", "attach", "sess_test", flag, "test prompt", "--api-url", server.URL}
					if mode != "default" {
						args = append(args, mode)
					}
					bearer := ""
					if auth == "signed in" {
						bearer = "test-topos"
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "TOPOS_TOKEN="+bearer, "AUTH_URL="+server.URL, "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
					out, err := command.CombinedOutput()
					wantError := ""
					switch {
					case readonly:
						wantError = "--readonly cannot be combined with --print"
					case auth == "missing":
						wantError = "not signed in for Topos"
					}
					wantRequests := int32(1)
					if wantError != "" {
						wantRequests = 0
						if err == nil || !strings.Contains(string(out), wantError) || strings.Contains(string(out), "test answer") {
							t.Errorf("rejected attach: err=%v, output=%s, want %q", err, out, wantError)
						}
					} else if err != nil || !strings.Contains(string(out), "test answer") {
						t.Errorf("writable attach failed: %v: %s", err, out)
					}
					if requests.Load() != wantRequests || turns.Load() != wantRequests {
						t.Errorf("attach requests/turns=%d/%d, want %d/%d", requests.Load(), turns.Load(), wantRequests, wantRequests)
					}
				})
			}
		}
	}
}
