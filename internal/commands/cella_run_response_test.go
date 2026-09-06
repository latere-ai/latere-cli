// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestOneShotRunResponseIdentity(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		for _, tc := range []struct {
			name, body string
			status     int
			valid      bool
		}{
			{"missing", `{}`, 200, false},
			{"null", `null`, 200, false},
			{"no content", "", 204, false},
			{"missing ID", `{"state":"cancelled"}`, 200, false},
			{"wrong ID", `{"run_id":"other","state":"cancelled"}`, 200, false},
			{"missing state", `{"run_id":"run-123"}`, 200, false},
			{"blank state", `{"run_id":"run-123","state":" \t"}`, 200, false},
			{"valid running", `{"run_id":"run-123","state":"running"}`, 200, true},
			{"valid cancelled", `{"run_id":"run-123","state":"cancelled"}`, 200, true},
			{"future state", `{"run_id":"run-123","state":"future-state"}`, 200, true},
			{"API failure", `{"code":"not_found","message":"run not found"}`, 404, false},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != method || r.URL.Path != "/v1/one-shot-runs/run-123" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer server.Close()
				client := &api.Client{BaseURL: server.URL, HTTP: server.Client()}
				var out oneShotRunDTO
				var err error
				if method == http.MethodGet {
					out, err = oneShotRunStatus(t.Context(), client, "run-123")
				} else {
					out, err = oneShotRunCancel(t.Context(), client, "run-123")
				}
				if tc.status == 404 {
					if apiErr, ok := errors.AsType[*api.APIError](err); !ok || apiErr.Status != 404 || apiErr.Code != "not_found" {
						t.Errorf("API failure changed: %v", err)
					}
				} else if tc.valid {
					if err != nil || out.RunID != "run-123" || out.State == "" {
						t.Errorf("valid response=%+v, %v", out, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "run response") {
					t.Errorf("invalid response returned %v", err)
				}
			})
		}
	}
}
