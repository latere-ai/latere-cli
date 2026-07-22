package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// countingRoundTripper records what the forwarder actually put on the wire to
// the upstream, so the test can tell a refused request apart from a truncated
// one that was relayed anyway.
type countingRoundTripper struct {
	called        bool
	bodyLen       int
	contentLength int64
}

func (rt *countingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.called = true
	b, _ := io.ReadAll(r.Body)
	rt.bodyLen = len(b)
	rt.contentLength = r.ContentLength
	return &http.Response{
		StatusCode: http.StatusOK,
		ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(`{}`)),
		ContentLength: 2,
	}, nil
}

// TestForwarderDoesNotSilentlyTruncateOversizedBody pins the cap behaviour: a
// request body past maxRequestBytes is refused, not cut down to the cap and
// forwarded. A truncated body carrying a matching Content-Length is
// self-consistent, so neither the caller nor the upstream can detect the loss,
// which is the worst possible failure mode for a relay.
func TestForwarderDoesNotSilentlyTruncateOversizedBody(t *testing.T) {
	const size = maxRequestBytes + 1

	rt := &countingRoundTripper{}
	f := &forwarder{
		ctx:      context.Background(),
		client:   &http.Client{Transport: rt},
		upstream: "http://upstream.invalid",
		out:      io.Discard,
	}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.handle(server)
	}()

	// Write the oversized request in the background: when the forwarder
	// refuses (or, before the fix, stops reading at the cap) the remaining
	// bytes have no reader, so this write must not block the assertions.
	go func() {
		fmt.Fprintf(client, "POST /v1/chat/completions HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", size)
		_, _ = client.Write(bytes.Repeat([]byte("a"), size))
	}()

	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read forwarder response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = client.Close()
	// f.handle runs in its own goroutine and owns the recorder fields; wait
	// for it to finish before reading them.
	<-done

	if rt.called {
		if rt.bodyLen < size && rt.contentLength == int64(rt.bodyLen) {
			t.Fatalf("upstream received %d of %d body bytes with a matching Content-Length: the forwarder relayed a silently truncated body",
				rt.bodyLen, size)
		}
		t.Fatalf("upstream was called with %d of %d body bytes; an oversized body must be refused before the upstream call",
			rt.bodyLen, size)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if !strings.Contains(string(respBody), "local.request_too_large") {
		t.Errorf("body = %q, want a local.request_too_large error", respBody)
	}
}
