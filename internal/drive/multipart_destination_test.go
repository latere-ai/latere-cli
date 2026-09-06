// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMultipartDestination(t *testing.T) {
	for _, tc := range []struct {
		name, want, returned string
		valid                bool
	}{
		{"matching", "files/item", "files/item", true},
		{"special characters", "files/ü ?#%/item", "files/ü ?#%/item", true},
		{"missing", "files/item", "", false},
		{"wrong", "files/item", "files/other", false},
		{"encoded instead of literal", "files/a b", "files/a%20b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var created, parts, completed, aborted atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/uploads":
					created.Add(1)
					var req struct{ Path string }
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path != tc.want {
						t.Errorf("requested path=%q error=%v", req.Path, err)
					}
					_ = json.NewEncoder(w).Encode(uploadSession{UploadID: "upload", Path: tc.returned, PartSize: 4, PartCount: 2, PartURLs: []string{"http://" + r.Host + "/part", "http://" + r.Host + "/part"}})
				case "/part":
					parts.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("ETag", "part")
				case "/api/v1/uploads/upload/complete":
					completed.Add(1)
					_ = json.NewEncoder(w).Encode(FileWriteResult{Path: tc.returned, Size: 7})
				case "/api/v1/uploads/upload":
					if r.Method != http.MethodDelete {
						t.Errorf("cleanup method=%s", r.Method)
					}
					aborted.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected endpoint %s", r.URL)
				}
			}))
			defer server.Close()
			out, err := New(server.URL, "synthetic-token").MultipartUpload(t.Context(), "me", tc.want, bytes.NewReader([]byte("payload")), 7, PutOptions{})
			wantParts, wantCompleted, wantAborted := int32(0), int32(0), int32(1)
			if tc.valid {
				wantParts, wantCompleted, wantAborted = 2, 1, 0
				if err != nil || out == nil || out.Path != tc.want {
					t.Errorf("valid session: %+v %v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "upload session destination") || out != nil {
				t.Errorf("invalid session: %+v %v", out, err)
			}
			if created.Load() != 1 || parts.Load() != wantParts || completed.Load() != wantCompleted || aborted.Load() != wantAborted {
				t.Errorf("created=%d parts=%d complete=%d abort=%d, want 1/%d/%d/%d", created.Load(), parts.Load(), completed.Load(), aborted.Load(), wantParts, wantCompleted, wantAborted)
			}
		})
	}
}
