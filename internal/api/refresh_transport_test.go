// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type refreshRoundTripFunc func(*http.Request) (*http.Response, error)

func (f refreshRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type trackedRefreshBody struct {
	reader       io.Reader
	read, closes int
}

func (b *trackedRefreshBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *trackedRefreshBody) Close() error { b.closes++; return nil }

func TestAuthRefreshTransportBoundsAndClosesResponses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status, size int
	}{
		{"complete", 200, 20},
		{"exact limit", 200, authRefreshResponseLimit},
		{"oversized", 200, 2 * authRefreshResponseLimit},
		{"redirect", 307, 20},
		{"auth failure", 401, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedRefreshBody{reader: strings.NewReader(strings.Repeat("x", tc.size))}
			original := &http.Response{StatusCode: tc.status, Body: body}
			transport := authRefreshTransport{base: refreshRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return original, nil
			})}
			got, err := transport.RoundTrip(nil)
			if tc.status != 200 {
				if got != original || err != nil || body.read != 0 || body.closes != 0 {
					t.Fatalf("redirect/error response changed: err=%v read=%d closes=%d", err, body.read, body.closes)
				}
				_ = got.Body.Close()
				return
			}
			if body.closes != 1 || body.read != min(tc.size, authRefreshResponseLimit+1) {
				t.Errorf("unbounded read or body leak: read=%d closes=%d", body.read, body.closes)
			}
			if tc.size > authRefreshResponseLimit {
				if err == nil || got != nil {
					t.Fatal("oversized response accepted")
				}
			} else {
				if err != nil || got == nil {
					t.Fatalf("valid response rejected: %v", err)
				}
				data, err := io.ReadAll(got.Body)
				_ = got.Body.Close()
				if err != nil || string(data) != strings.Repeat("x", tc.size) {
					t.Error("response body changed")
				}
			}
		})
	}
}

func TestAuthRefreshTransportPreservesErrors(t *testing.T) {
	transportErr := errors.New("transport failed")
	transport := authRefreshTransport{base: refreshRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	if resp, err := transport.RoundTrip(nil); resp != nil || !errors.Is(err, transportErr) {
		t.Fatalf("transport error lost: %v", err)
	}
	reader, writer := io.Pipe()
	_ = writer.CloseWithError(io.ErrUnexpectedEOF)
	body := &trackedRefreshBody{reader: reader}
	transport.base = refreshRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body}, nil
	})
	if resp, err := transport.RoundTrip(nil); resp != nil || !errors.Is(err, io.ErrUnexpectedEOF) || body.closes != 1 {
		t.Fatalf("read error lost or body leaked: err=%v closes=%d", err, body.closes)
	}
	_ = reader.Close()
}
