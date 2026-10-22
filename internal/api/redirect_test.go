// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The CLI's client follows at most ten redirects, as Go's default does.
func TestRedirectLoopRemainsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/loop")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/loop", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := HTTPClient().Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || calls.Load() != 10 {
		t.Errorf("redirect loop: %v, %d requests; want error after 10 requests", err, calls.Load())
	}
}
