package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRunFailsFastOnAuthError: a non-retryable bearer error (e.g. not signed
// in) must return immediately, not enter the reconnect-with-backoff loop.
func TestRunFailsFastOnAuthError(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"m1"}]}`))
		}
	}))
	defer local.Close()

	authErr := errors.New("not signed in for Lux; run `latere login`")
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			LuxURL:      "http://127.0.0.1:1",
			Bearer:      func(context.Context) (string, error) { return "", authErr },
			Runtime:     RuntimeOllama,
			UpstreamURL: local.URL,
			Out:         io.Discard,
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, authErr) {
			t.Errorf("Run err = %v, want the auth error surfaced", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not fail fast on a non-retryable auth error (it looped)")
	}
}

func TestDefaultURL(t *testing.T) {
	cases := map[string]string{
		RuntimeOllama:   "http://localhost:11434",
		RuntimeVLLM:     "http://localhost:8000",
		RuntimeLMStudio: "http://localhost:1234",
		RuntimeLlamaCPP: "http://localhost:8080",
		RuntimeMLX:      "http://localhost:8080",
		"unknown":       "http://localhost:11434",
	}
	for runtime, want := range cases {
		if got := DefaultURL(runtime); got != want {
			t.Errorf("DefaultURL(%q) = %q, want %q", runtime, got, want)
		}
	}
}

func TestNodeIDStableAndPersisted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	first := NodeID()
	if !strings.HasPrefix(first, "node-") {
		t.Errorf("NodeID = %q, want node- prefix", first)
	}
	if second := NodeID(); second != first {
		t.Errorf("NodeID not stable: %q vs %q", first, second)
	}
	// Persisted to disk and reused across the helper's path.
	if got := nodeIDPath(); got == "" || !strings.Contains(got, "tunnel-node-id") {
		t.Errorf("nodeIDPath = %q", got)
	}
}

func TestNodeIDPathUsesHomeWhenNoXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	got := nodeIDPath()
	if got == "" || !strings.Contains(got, "latere") {
		t.Errorf("nodeIDPath without XDG = %q, want a path under the home config dir", got)
	}
}

func TestDiscoverBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	if _, err := discover(context.Background(), srv.Client(), RuntimeVLLM, srv.URL, nil); err == nil {
		t.Error("discover with bad JSON = nil error, want decode error")
	}
}

func TestHandleClosedStream(t *testing.T) {
	f := &forwarder{ctx: context.Background(), client: &http.Client{}, upstream: "http://127.0.0.1:1"}
	c1, c2 := net.Pipe()
	_ = c2.Close() // peer hangs up before sending a request
	// handle must return without panicking on the ReadRequest error.
	done := make(chan struct{})
	go func() { f.handle(c1); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("handle did not return on a closed stream")
	}
}

func TestWriteErrorProducesBadGateway(t *testing.T) {
	c1, c2 := net.Pipe()
	go func() {
		writeError(c1, io.EOF)
		_ = c1.Close()
	}()
	resp, err := http.ReadResponse(bufio.NewReader(c2), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "local.unreachable") {
		t.Errorf("body = %s, want local.unreachable", body)
	}
}

func TestForwarderUnreachableUpstream(t *testing.T) {
	f := &forwarder{ctx: context.Background(), client: &http.Client{Timeout: time.Second}, upstream: "http://127.0.0.1:1"}
	c1, c2 := net.Pipe()
	go f.handle(c1)

	req, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", strings.NewReader(`{}`))
	go func() { _ = req.Write(c2) }()
	resp, err := http.ReadResponse(bufio.NewReader(c2), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for unreachable upstream", resp.StatusCode)
	}
}

func TestForwarderForwardsToUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	}))
	defer upstream.Close()

	f := &forwarder{ctx: context.Background(), client: &http.Client{}, upstream: upstream.URL}
	c1, c2 := net.Pipe()
	go f.handle(c1)
	req, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", strings.NewReader(`{}`))
	go func() { _ = req.Write(c2) }()
	resp, err := http.ReadResponse(bufio.NewReader(c2), req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok:/v1/chat/completions" {
		t.Errorf("body = %q", body)
	}
}

// A served request is logged with method, path, model, and status so the
// operator can watch traffic while `latere lux serve` is running.
func TestForwarderLogsTraffic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"gemma4:latest"`) {
			t.Errorf("upstream body = %q, want the forwarded model body", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	var logBuf bytes.Buffer
	f := &forwarder{ctx: context.Background(), client: &http.Client{}, upstream: upstream.URL, out: &logBuf}
	c1, c2 := net.Pipe()
	done := make(chan struct{})
	go func() { f.handle(c1); close(done) }()
	req, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", strings.NewReader(`{"model":"gemma4:latest"}`))
	go func() { _ = req.Write(c2) }()
	resp, err := http.ReadResponse(bufio.NewReader(c2), req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	<-done // handle logs after the response is written

	line := logBuf.String()
	for _, want := range []string{"POST /v1/chat/completions", "model=gemma4:latest", "201"} {
		if !strings.Contains(line, want) {
			t.Errorf("traffic log %q missing %q", line, want)
		}
	}
}

func TestGetJSONErrors(t *testing.T) {
	// Non-200 surfaces an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	var dst struct{}
	if err := getJSON(context.Background(), srv.Client(), srv.URL, &dst); err == nil {
		t.Error("getJSON on 500 = nil error, want error")
	}
	// Unreachable host surfaces an error.
	if err := getJSON(context.Background(), &http.Client{Timeout: time.Second}, "http://127.0.0.1:1/x", &dst); err == nil {
		t.Error("getJSON on unreachable = nil error, want error")
	}
}

func TestRunSessionDiscoverError(t *testing.T) {
	// An unreachable upstream makes discovery fail, so runSession returns
	// before any dial.
	_, err := runSession(context.Background(), Options{
		LuxURL:      "http://127.0.0.1:1",
		Bearer:      func(context.Context) (string, error) { return "t", nil },
		Runtime:     RuntimeOllama,
		UpstreamURL: "http://127.0.0.1:1",
		Out:         io.Discard,
	})
	if err == nil {
		t.Error("runSession with unreachable upstream = nil error, want discovery error")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// Upstream unreachable so each session fails fast; Run should keep
	// retrying then return when the context expires.
	err := Run(ctx, Options{
		LuxURL:            "http://127.0.0.1:1",
		Bearer:            func(context.Context) (string, error) { return "t", nil },
		Runtime:           RuntimeOllama,
		UpstreamURL:       "http://127.0.0.1:1",
		HeartbeatInterval: time.Second,
		Out:               io.Discard,
	})
	if err == nil {
		t.Error("Run = nil error on context expiry, want ctx error")
	}
}

func TestHeartbeatLoopStopsOnContextCancel(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		heartbeatLoop(ctx, c1, Options{
			HeartbeatInterval: 10 * time.Millisecond,
			Bearer:            func(context.Context) (string, error) { return "tok", nil },
		})
		close(done)
	}()
	// Drain the pipe so heartbeat writes don't block.
	go io.Copy(io.Discard, c2)
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("heartbeatLoop did not stop on context cancel")
	}
}
