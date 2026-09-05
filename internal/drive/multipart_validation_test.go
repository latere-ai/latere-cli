// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMultipartUploadRejectsMalformedSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		id        string
		partSize  int
		partCount int
		urlCount  int
	}{
		{"no_parts", "upload", 4, 0, 0},
		{"too_few_parts", "upload", 4, 1, 1},
		{"too_many_parts", "upload", 4, 3, 3},
		{"missing_url", "upload", 4, 2, 1},
		{"zero_part_size", "upload", 0, 2, 2},
		{"negative_part_size", "upload", -4, 2, 2},
		{"missing_id", "", 4, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var puts, completes, aborts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodDelete:
					aborts.Add(1)
					if r.URL.Path != "/api/v1/uploads/"+tc.id || tc.id == "" {
						t.Errorf("unexpected abort: %s", r.URL.Path)
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut:
					puts.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("ETag", `"part-etag"`)
				case strings.HasSuffix(r.URL.Path, "/complete"):
					completes.Add(1)
					_ = json.NewEncoder(w).Encode(FileWriteResult{Size: 8})
				default:
					urls := make([]string, tc.urlCount)
					for i := range urls {
						urls[i] = "http://" + r.Host + "/part"
					}
					_ = json.NewEncoder(w).Encode(uploadSession{
						UploadID: tc.id, PartSize: tc.partSize, PartCount: tc.partCount, PartURLs: urls,
					})
				}
			}))
			defer srv.Close()
			_, err := New(srv.URL, "tok").MultipartUpload(context.Background(), "me", "files/data",
				bytes.NewReader([]byte("12345678")), 8, PutOptions{})
			if err == nil || !strings.Contains(err.Error(), "malformed upload session") {
				t.Errorf("error = %v, want malformed session", err)
			}
			if puts.Load() != 0 || completes.Load() != 0 {
				t.Errorf("invalid session used: %d part PUTs, %d completions", puts.Load(), completes.Load())
			}
			wantAborts := int32(1)
			if tc.id == "" {
				wantAborts = 0
			}
			if aborts.Load() != wantAborts {
				t.Errorf("aborts = %d, want %d", aborts.Load(), wantAborts)
			}
		})
	}
}

func TestMultipartUploadRejectsNonPositiveSize(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer srv.Close()
	for _, size := range []int64{0, -1} {
		_, err := New(srv.URL, "tok").MultipartUpload(context.Background(), "me", "files/data",
			bytes.NewReader(nil), size, PutOptions{})
		if err == nil || !strings.Contains(err.Error(), "size must be positive") {
			t.Errorf("size %d: error = %v", size, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("invalid size sent %d HTTP requests", calls.Load())
	}
}
