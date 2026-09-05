// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type emptyUploadBody struct{ closed bool }

func (*emptyUploadBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *emptyUploadBody) Close() error           { b.closed = true; return nil }

func TestPutEmptyBodyDeclaresContentLength(t *testing.T) {
	for _, kind := range []string{"nil", "reader", "closer"} {
		t.Run(kind, func(t *testing.T) {
			var body io.Reader
			tracked := &emptyUploadBody{}
			switch kind {
			case "reader":
				body = strings.NewReader("")
			case "closer":
				body = tracked
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ContentLength != 0 || r.Header.Get("Content-Length") != "0" || len(r.TransferEncoding) != 0 {
					t.Errorf("empty upload length=%d header=%q encoding=%v", r.ContentLength, r.Header.Get("Content-Length"), r.TransferEncoding)
					http.Error(w, "Content-Length is required", http.StatusLengthRequired)
					return
				}
				if r.Header.Get("If-None-Match") != "*" || r.Header.Get("Content-Type") != "text/plain" {
					t.Error("empty upload lost its upload options")
				}
				_, _ = io.WriteString(w, `{"path":"files/empty","size":0}`)
			}))
			defer server.Close()
			result, err := New(server.URL, "token").Put(t.Context(), "me", "files/empty", body, 0, PutOptions{CreateOnly: true, ContentType: "text/plain"})
			if err != nil {
				t.Fatalf("empty upload: %v", err)
			}
			if result.Size != 0 {
				t.Errorf("uploaded size = %d", result.Size)
			}
			if kind == "closer" && !tracked.closed {
				t.Error("upload body was not closed")
			}
		})
	}
}

func TestPutZeroLengthRedirectStaysEmpty(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 || r.ContentLength != 0 {
			t.Errorf("zero-length request changed during redirect: %q, length=%d, %v", body, r.ContentLength, err)
		}
		if r.Method != http.MethodPut {
			t.Errorf("redirect changed upload method: %s", r.Method)
		}
		if r.URL.Path != "/redirected" {
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `{"path":"files/empty","size":0}`)
	}))
	defer server.Close()
	// A replayable reader must not restore bytes beyond the declared zero size.
	_, err := New(server.URL, "token").Put(t.Context(), "me", "files/empty", strings.NewReader("undeclared"), 0, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want upload plus redirect", requests.Load())
	}
}
