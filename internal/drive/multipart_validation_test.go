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
	"strconv"
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

func TestMultipartUploadRejectsIncompleteResponses(t *testing.T) {
	for _, stage := range []string{"create", "complete"} {
		for _, state := range []string{"valid", "truncated", "extra JSON"} {
			t.Run(stage+"/"+state, func(t *testing.T) {
				var puts, completes, aborts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload []byte
					current := "create"
					switch {
					case r.Method == http.MethodDelete:
						aborts.Add(1)
						if r.URL.Path != "/api/v1/uploads/test-upload" {
							t.Errorf("unexpected abort: %s", r.URL.Path)
						}
						w.WriteHeader(http.StatusNoContent)
						return
					case r.Method == http.MethodPut:
						puts.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						w.Header().Set("ETag", `"part-etag"`)
						return
					case strings.HasSuffix(r.URL.Path, "/complete"):
						completes.Add(1)
						current, payload = "complete", []byte(`{"path":"files/data","size":8}`)
					default:
						payload, _ = json.Marshal(uploadSession{UploadID: "test-upload", PartSize: 4, PartCount: 2, PartURLs: []string{"http://" + r.Host + "/part1", "http://" + r.Host + "/part2"}})
					}
					if current == stage {
						if state == "truncated" {
							w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
						}
						if state == "extra JSON" {
							payload = append(payload, []byte(" {}")...)
						}
					}
					_, _ = w.Write(payload)
				}))
				defer server.Close()
				_, err := New(server.URL, "test-token").MultipartUpload(t.Context(), "me", "files/data", strings.NewReader("12345678"), 8, PutOptions{})
				invalid := state != "valid"
				if (err != nil) != invalid {
					t.Errorf("upload error = %v, invalid response=%t", err, invalid)
				}
				wantPuts, wantCompletes, wantAborts := int32(2), int32(1), int32(0)
				if invalid {
					wantAborts = 1
					if stage == "create" {
						wantPuts, wantCompletes = 0, 0
					}
				}
				if puts.Load() != wantPuts || completes.Load() != wantCompletes || aborts.Load() != wantAborts {
					t.Errorf("put/complete/abort calls=%d/%d/%d, want %d/%d/%d", puts.Load(), completes.Load(), aborts.Load(), wantPuts, wantCompletes, wantAborts)
				}
			})
		}
	}
}
