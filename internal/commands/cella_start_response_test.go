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

func TestCellaStartResponseRequiresID(t *testing.T) {
	for _, detached := range []bool{false, true} {
		field := "command_id"
		if detached {
			field = "run_id"
		}
		for _, tc := range []struct {
			name, body string
			status     int
		}{
			{"missing", `{}`, 200},
			{"empty", `{"command_id":"","run_id":""}`, 200},
			{"null", `null`, 200},
			{"no content", "", 204},
			{"valid", `{"command_id":"cmd-123","run_id":"run-123"}`, 200},
			{"API failure", `{"error":"unavailable"}`, 503},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer server.Close()
				client := &api.Client{BaseURL: server.URL, HTTP: server.Client(), Token: "synthetic-token"}
				var err error
				if detached {
					_, err = oneShotRunDetached(t.Context(), client, []string{"echo"}, nil, "", "", 0, "", "", 600, nil)
				} else {
					_, err = startCommand(t.Context(), client, "dev", []string{"echo"}, nil, "", nil)
				}
				switch tc.name {
				case "valid":
					if err != nil {
						t.Errorf("valid start failed: %v", err)
					}
				case "API failure":
					if _, ok := errors.AsType[*api.APIError](err); !ok {
						t.Errorf("API error replaced: %v", err)
					}
				default:
					if err == nil || !strings.Contains(err.Error(), "missing "+field) || !strings.Contains(err.Error(), "may have started") {
						t.Errorf("incomplete start response returned %v", err)
					}
				}
			})
		}
	}
}
