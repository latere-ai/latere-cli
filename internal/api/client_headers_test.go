// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// DoWithHeaders must attach caller-supplied headers (e.g. Idempotency-Key) to
// the outbound request.
func TestDoWithHeadersSetsIdempotencyKey(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	var out map[string]any
	if err := c.DoWithHeaders(context.Background(), http.MethodPost, "/v1/sandboxes", nil,
		"application/json", map[string]string{"Idempotency-Key": "abc"}, &out); err != nil {
		t.Fatal(err)
	}
	if got != "abc" {
		t.Fatalf("Idempotency-Key = %q, want abc", got)
	}
}
