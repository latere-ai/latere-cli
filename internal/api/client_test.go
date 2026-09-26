// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientErrorsPreserveHTTPStatus(t *testing.T) {
	for _, body := range []string{
		`{"status":200,"code":"unavailable","message":"try later","request_id":"req-test"}`,
		`{"Status":404,"code":"unavailable","message":"try later","request_id":"req-test"}`,
		`{"STATUS":0,"code":"unavailable","message":"try later","request_id":"req-test"}`,
		`{"code":"unavailable","message":"try later","request_id":"req-test"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			client := &Client{BaseURL: server.URL, HTTP: server.Client()}
			err := client.GetJSON(t.Context(), "/test", nil)
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok {
				t.Fatalf("expected structured HTTP error, got %v", err)
			}
			if apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != "unavailable" || apiErr.Message != "try later" || apiErr.ReqID != "req-test" {
				t.Errorf("HTTP error fields = %+v", *apiErr)
			}
		})
	}
}

func TestClientRedirectLoopRemainsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/loop")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	err := NewClient(server.URL).GetJSON(t.Context(), "/loop", nil)
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || calls.Load() != 10 {
		t.Errorf("redirect loop: %v, %d requests; want error after 10 requests", err, calls.Load())
	}
}
