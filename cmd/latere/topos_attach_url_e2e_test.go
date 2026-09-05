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

func TestToposAttachPreservesSessionIDInURLE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, operation := range []string{"attach", "start"} {
		for _, id := range []string{"sess_plain", "sess/child", "sess?mode=rw", "sess#fragment", "sess%2Fchild", "sess with spaces"} {
			t.Run(operation+"/"+id, func(t *testing.T) {
				root := t.TempDir()
				var attaches atomic.Int32
				mux := http.NewServeMux()
				mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "agent_id": "test-agent", "status": "running"})
				})
				mux.HandleFunc("GET /v1/sessions/{id}/attach", func(w http.ResponseWriter, r *http.Request) {
					attaches.Add(1)
					wantMode := ""
					if operation == "start" {
						wantMode = "ro"
					}
					q := r.URL.Query()
					if r.PathValue("id") != id || q.Get("since") != "0" || q.Get("mode") != wantMode || r.Header.Get("Authorization") != "Bearer test-topos" {
						t.Errorf("attach changed session or options: id=%q, query=%q", r.PathValue("id"), r.URL.RawQuery)
						http.Error(w, "wrong attach target", http.StatusBadRequest)
						return
					}
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.CloseNow() }()
					if operation == "attach" {
						_, data, err := conn.Read(r.Context())
						var turn struct {
							Type string `json:"type"`
							Text string `json:"text"`
						}
						if err != nil || json.Unmarshal(data, &turn) != nil || turn.Type != "user_turn" || turn.Text != "test prompt" {
							t.Errorf("attach lost prompt: %v: %s", err, data)
						}
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
				})
				server := httptest.NewServer(mux)
				defer server.Close()
				argument := id
				if operation == "start" {
					argument = "test-agent"
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "topos", "session", operation, argument, "-p", "test prompt", "--api-url", server.URL)
				command.Env = append(os.Environ(), "TOPOS_TOKEN=test-topos", "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				out, err := command.CombinedOutput()
				if err != nil || !strings.Contains(string(out), "test answer") || attaches.Load() != 1 {
					t.Errorf("attach failed: err=%v, attaches=%d, output=%s", err, attaches.Load(), out)
				}
			})
		}
	}
}
