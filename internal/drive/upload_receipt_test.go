// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUploadReceipt(t *testing.T) {
	for _, mode := range []string{"single", "empty", "multipart", "leading slash"} {
		size := int64(7)
		if mode == "empty" {
			size = 0
		}
		valid := fmt.Sprintf(`{"path":"files/item","size":%d,"checksum":"opaque","url":"/item"}`, size)
		for _, tc := range []struct {
			name, body string
			valid      bool
		}{
			{"valid", valid, true},
			{"null", "null", false},
			{"empty", "{}", false},
			{"missing path", strings.Replace(valid, `"path":"files/item",`, "", 1), false},
			{"wrong path", strings.Replace(valid, "files/item", "files/other", 1), false},
			{"missing size", strings.Replace(valid, fmt.Sprintf(`"size":%d,`, size), "", 1), false},
			{"null size", strings.Replace(valid, fmt.Sprintf(`"size":%d`, size), `"size":null`, 1), false},
			{"short size", strings.Replace(valid, fmt.Sprintf(`"size":%d`, size), fmt.Sprintf(`"size":%d`, size-1), 1), false},
			{"extra size", strings.Replace(valid, fmt.Sprintf(`"size":%d`, size), fmt.Sprintf(`"size":%d`, size+1), 1), false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var receipts, parts, aborts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodDelete:
						aborts.Add(1)
						w.WriteHeader(http.StatusNoContent)
					case r.URL.Path == "/api/v1/uploads":
						_ = json.NewEncoder(w).Encode(uploadSession{UploadID: "upload", Path: "files/item", PartSize: 4, PartCount: 2, PartURLs: []string{"http://" + r.Host + "/part", "http://" + r.Host + "/part"}})
					case r.URL.Path == "/part":
						parts.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						w.Header().Set("ETag", "part")
					default:
						receipts.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						_, _ = io.WriteString(w, tc.body)
					}
				}))
				defer server.Close()
				c := New(server.URL, "synthetic-token")
				data := bytes.NewReader(bytes.Repeat([]byte("x"), int(size)))
				var out *FileWriteResult
				var err error
				if mode == "multipart" {
					out, err = c.MultipartUpload(t.Context(), "me", "files/item", data, size, PutOptions{})
				} else {
					path := "files/item"
					if mode == "leading slash" {
						path = "/" + path
					}
					out, err = c.Put(t.Context(), "me", path, data, size, PutOptions{})
				}
				if tc.valid {
					if err != nil || out == nil || out.Path != "files/item" || out.Size != size || out.Checksum != "opaque" || out.URL != "/item" {
						t.Errorf("valid receipt: %+v %v", out, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "upload receipt") || out != nil {
					t.Errorf("invalid receipt: %+v %v", out, err)
				}
				wantParts, wantAborts := int32(0), int32(0)
				if mode == "multipart" {
					wantParts = 2
					if !tc.valid {
						wantAborts = 1
					}
				}
				if receipts.Load() != 1 || parts.Load() != wantParts || aborts.Load() != wantAborts {
					t.Errorf("receipts=%d parts=%d aborts=%d, want 1/%d/%d", receipts.Load(), parts.Load(), aborts.Load(), wantParts, wantAborts)
				}
			})
		}
	}
}
