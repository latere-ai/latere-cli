// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreflightDiscoversModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1:8b"},{"name":"qwen3:4b"}]}`))
	}))
	defer srv.Close()

	models, err := Preflight(context.Background(), RuntimeOllama, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Errorf("models = %v", models)
	}
	allowed, err := Preflight(context.Background(), RuntimeOllama, srv.URL, []string{"qwen3:4b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 1 || allowed[0] != "qwen3:4b" {
		t.Errorf("allowlist filter = %v", allowed)
	}
}

func TestPreflightUnreachableRuntime(t *testing.T) {
	// A closed port: the probe must fail fast with an error, not hang.
	if _, err := Preflight(context.Background(), RuntimeOllama, "http://127.0.0.1:1", nil); err == nil {
		t.Fatal("want error for unreachable runtime")
	}
}
