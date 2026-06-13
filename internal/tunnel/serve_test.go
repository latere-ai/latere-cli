package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

func TestToWS(t *testing.T) {
	cases := map[string]string{
		"https://lux.latere.ai":  "wss://lux.latere.ai",
		"http://localhost:8080/": "ws://localhost:8080",
		"wss://already":          "wss://already",
	}
	for in, want := range cases {
		if got := toWS(in); got != want {
			t.Errorf("toWS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverOllamaAndFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("ollama probe path = %q, want /api/tags", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1:8b"},{"name":"qwen2.5:14b"}]}`))
	}))
	defer srv.Close()

	got, err := discover(context.Background(), srv.Client(), RuntimeOllama, srv.URL, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 models", got)
	}
	// Allowlist filters.
	got, err = discover(context.Background(), srv.Client(), RuntimeOllama, srv.URL, []string{"qwen2.5:14b"})
	if err != nil {
		t.Fatalf("discover filtered: %v", err)
	}
	if len(got) != 1 || got[0] != "qwen2.5:14b" {
		t.Errorf("filtered = %v, want [qwen2.5:14b]", got)
	}
}

func TestDiscoverOpenAICompat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("openai probe path = %q, want /v1/models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"my-model"}]}`))
	}))
	defer srv.Close()
	got, err := discover(context.Background(), srv.Client(), RuntimeVLLM, srv.URL, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1 || got[0] != "my-model" {
		t.Errorf("got %v, want [my-model]", got)
	}
}

// TestRunForwardsRequest exercises the full serve path: discovery, dial,
// descriptor handshake, and forwarding an inbound request stream to the
// local runtime.
func TestRunForwardsRequest(t *testing.T) {
	// Fake local runtime: serves the discovery probe and the forwarded call.
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"m1"}]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ran":` + string(body) + `,"path":"` + r.URL.Path + `"}`))
	}))
	defer local.Close()

	type result struct {
		status int
		body   string
		desc   Descriptor
	}
	done := make(chan result, 1)

	// Fake luxd: accept the tunnel, read the descriptor, then drive one
	// request down a stream and capture the response.
	lux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"lux.tunnel.v1"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(-1)
		nc := websocket.NetConn(r.Context(), c, websocket.MessageBinary)
		sess, err := yamux.Server(nc, yamuxConfig())
		if err != nil {
			return
		}
		defer sess.Close()
		ctrl, err := sess.AcceptStream()
		if err != nil {
			return
		}
		line, err := bufio.NewReader(ctrl).ReadBytes('\n')
		if err != nil {
			return
		}
		var desc Descriptor
		_ = json.Unmarshal(line, &desc)

		stream, err := sess.OpenStream()
		if err != nil {
			return
		}
		req, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions",
			strings.NewReader(`{"model":"m1"}`))
		req.ContentLength = int64(len(`{"model":"m1"}`))
		_ = req.Write(stream)
		resp, err := http.ReadResponse(bufio.NewReader(stream), req)
		if err != nil {
			return
		}
		b, _ := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: string(b), desc: desc}
	}))
	defer lux.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Options{
			LuxURL:      lux.URL,
			Bearer:      func(context.Context) (string, error) { return "test-bearer", nil },
			Runtime:     RuntimeOllama,
			UpstreamURL: local.URL,
			NodeID:      "test-node",
			Out:         io.Discard,
		})
	}()

	select {
	case got := <-done:
		if got.status != http.StatusOK {
			t.Errorf("status = %d, want 200", got.status)
		}
		if got.desc.NodeID != "test-node" || len(got.desc.Models) != 1 || got.desc.Models[0] != "m1" {
			t.Errorf("descriptor = %+v, want node test-node, models [m1]", got.desc)
		}
		want := `{"ran":{"model":"m1"},"path":"/v1/chat/completions"}`
		if got.body != want {
			t.Errorf("body = %q, want %q", got.body, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forwarded request")
	}
}
