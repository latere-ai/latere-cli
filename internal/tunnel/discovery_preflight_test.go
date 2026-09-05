// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestPreflightRequiresCompleteBoundedJSON(t *testing.T) {
	for _, runtime := range []string{RuntimeOllama, RuntimeOpenAI} {
		for _, state := range []string{"complete", "whitespace", "at limit", "over limit", "short content length", "interrupted chunks", "extra JSON", "trailing garbage"} {
			t.Run(runtime+"/"+state, func(t *testing.T) {
				payload := `{"models":[{"name":"test-model"}]}`
				path := "/api/tags"
				if runtime == RuntimeOpenAI {
					payload = `{"data":[{"id":"test-model"}]}`
					path = "/v1/models"
				}
				valid := state == "complete" || state == "whitespace" || state == "at limit"
				switch state {
				case "whitespace":
					payload += " \r\n\t"
				case "at limit", "over limit":
					payload += strings.Repeat(" ", (1<<20)-len(payload))
					if state == "over limit" {
						payload += " "
					}
				case "extra JSON":
					payload += " {}"
				case "trailing garbage":
					payload += " garbage"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != path {
						t.Errorf("probe path = %q, want %q", r.URL.Path, path)
					}
					if state == "short content length" {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					_, _ = w.Write([]byte(payload))
					if state == "interrupted chunks" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				}))
				defer server.Close()
				models, err := Preflight(t.Context(), runtime, server.URL, nil)
				if valid {
					if err != nil || len(models) != 1 || models[0] != "test-model" {
						t.Fatalf("valid discovery: models=%v, err=%v", models, err)
					}
				} else if err == nil || len(models) != 0 {
					t.Fatalf("invalid discovery accepted: models=%v, err=%v", models, err)
				}
			})
		}
	}
}

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
