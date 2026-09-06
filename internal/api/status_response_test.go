// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPostJSONWithStatusRequiresCompleteResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body       string
		status           int
		truncated, valid bool
	}{
		{name: "success", body: `{"result":"ok"}`, status: 201, valid: true},
		{name: "structured failure", body: `{"result":"cleanup failed"}`, status: 500, valid: true},
		{name: "malformed", body: `{"result":`, status: 500},
		{name: "trailing value", body: `{"result":"ok"}{}`, status: 500},
		{name: "trailing junk", body: `{"result":"ok"}junk`, status: 500},
		{name: "truncated transport", body: `{"result":"ok"}`, status: 500, truncated: true},
		{name: "other error status", body: `{"code":"unavailable","message":"try later"}`, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.truncated {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.body)+10))
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := &Client{BaseURL: server.URL, HTTP: server.Client()}
			var out json.RawMessage
			status, err := client.PostJSONWithStatus(t.Context(), "/runs", map[string]string{"argv": "echo"}, &out, 500)
			if status != tc.status || (err == nil) != tc.valid {
				t.Errorf("status=%d err=%v; want status=%d valid=%v", status, err, tc.status, tc.valid)
			}
			if tc.valid && string(out) != tc.body {
				t.Errorf("result=%s, want %s", out, tc.body)
			}
			if tc.status == 503 {
				if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != 503 || apiErr.Code != "unavailable" || apiErr.Message != "try later" {
					t.Errorf("API error lost: %v", err)
				}
			}
		})
	}
}

func TestPostJSONWithStatusRefreshesOnce(t *testing.T) {
	var requests atomic.Int32
	const body = `{"argv":["echo","hello"]}`
	result := strings.Repeat("output", 5000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		received, err := io.ReadAll(r.Body)
		if err != nil || string(received) != body {
			t.Errorf("attempt %d body=%q, err=%v", attempt, received, err)
		}
		wantToken := "Bearer old"
		if attempt > 1 {
			wantToken = "Bearer fresh"
		}
		if r.Header.Get("Authorization") != wantToken {
			t.Errorf("attempt %d used wrong token", attempt)
		}
		if attempt == 1 {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": result})
	}))
	defer server.Close()
	var refreshes int
	client := &Client{BaseURL: server.URL, HTTP: server.Client(), Token: "old", Refresh: func(_ context.Context) (string, bool) { refreshes++; return "fresh", true }}
	var out struct{ Result string }
	status, err := client.PostJSONWithStatus(t.Context(), "/runs", json.RawMessage(body), &out, 500)
	if err != nil || status != 500 || out.Result != result || requests.Load() != 2 || refreshes != 1 {
		t.Errorf("status=%d err=%v output bytes=%d requests=%d refreshes=%d", status, err, len(out.Result), requests.Load(), refreshes)
	}
}
