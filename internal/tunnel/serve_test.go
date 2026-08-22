package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// recordingRoundTripper fails the test if the forwarder ever dials the
// upstream; it asserts a body read error stops the relay before forwarding.
type recordingRoundTripper struct{ called bool }

func (rt *recordingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.called = true
	return nil, fmt.Errorf("upstream must not be called")
}

// truncatedConn is a net.Conn whose reads deliver a fixed request head plus a
// partial body and then return an error, simulating a connection that drops
// mid-body. Writes are captured so the test can read the forwarder's response.
type truncatedConn struct {
	read    *bytes.Reader
	readErr error
	written bytes.Buffer
	done    chan struct{}
}

func (c *truncatedConn) Read(p []byte) (int, error) {
	n, err := c.read.Read(p)
	if err == io.EOF {
		// Surface a short-read error instead of clean EOF: the body was
		// declared longer than what arrived.
		return n, c.readErr
	}
	return n, err
}
func (c *truncatedConn) Write(p []byte) (int, error) { return c.written.Write(p) }
func (c *truncatedConn) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return nil
}
func (c *truncatedConn) LocalAddr() net.Addr              { return fakeAddr{} }
func (c *truncatedConn) RemoteAddr() net.Addr             { return fakeAddr{} }
func (c *truncatedConn) SetDeadline(time.Time) error      { return nil }
func (c *truncatedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *truncatedConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake" }

// TestForwarderRejectsTruncatedBody verifies that when the inbound request
// body cannot be fully read (a short read against a larger declared
// Content-Length), handle responds with a 502 local.unreachable error and
// does not forward a silently truncated body to the upstream.
func TestForwarderRejectsTruncatedBody(t *testing.T) {
	// Declare 1000 body bytes but only deliver 4, then fail the read.
	raw := "POST /v1/chat/completions HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n{\"m\""
	conn := &truncatedConn{
		read:    bytes.NewReader([]byte(raw)),
		readErr: io.ErrUnexpectedEOF,
		done:    make(chan struct{}),
	}

	rt := &recordingRoundTripper{}
	f := &forwarder{
		ctx:      context.Background(),
		client:   &http.Client{Transport: rt},
		upstream: "http://upstream.invalid",
		out:      io.Discard,
	}

	f.handle(conn)

	if rt.called {
		t.Error("upstream was called with a truncated body; relay should have failed loudly instead")
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(conn.written.Bytes())), nil)
	if err != nil {
		t.Fatalf("read forwarder response: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "local.unreachable") {
		t.Errorf("body = %q, want a local.unreachable error", b)
	}
}

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

	ctx := t.Context()
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
