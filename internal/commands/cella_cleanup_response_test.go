// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestOneShotRunCleanupResponse(t *testing.T) {
	output := strings.Repeat("result\n", 5000)
	for _, code := range []int{0, 7} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "run-test", "state": "cleanup_failed", "exit_code": code, "stdout": output, "stderr": "warning\n", "cleanup_error": "delete denied"})
		}))
		client := &api.Client{BaseURL: server.URL, HTTP: server.Client()}
		out, err := oneShotRun(t.Context(), client, []string{"echo"}, nil, "", "", 0, "", "", 600, nil)
		server.Close()
		if err != nil {
			t.Fatalf("cleanup result lost: %v", err)
		}
		if out.Stdout != output || out.Stderr != "warning\n" || out.CleanupError != "delete denied" || out.ExitCode == nil || *out.ExitCode != code {
			t.Errorf("cleanup result changed: stdout bytes=%d, exit=%v, cleanup=%q", len(out.Stdout), out.ExitCode, out.CleanupError)
		}
	}
}

func TestOneShotRunPreservesOtherServerFailures(t *testing.T) {
	for _, body := range []string{`{"code":"runtime","message":"create failed"}`, `{"run_id":"run-test","state":"exited","exit_code":0}`, `{"state":"cleanup_failed","cleanup_error":"missing run id"}`, `null`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(body))
		}))
		client := &api.Client{BaseURL: server.URL, HTTP: server.Client()}
		_, err := oneShotRun(t.Context(), client, []string{"echo"}, nil, "", "", 0, "", "", 600, nil)
		server.Close()
		if apiErr, ok := errors.AsType[*api.APIError](err); !ok || apiErr.Status != 500 {
			t.Errorf("server failure %s became %v", body, err)
		}
	}
}
