// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeleteRequiresCompletion(t *testing.T) {
	for _, operation := range []struct {
		name, path, query string
		run               func(*Client) error
	}{
		{"trash", "/api/v1/files/me/files/item", "", func(c *Client) error { return c.Delete(t.Context(), "me", "files/item", false, 0) }},
		{"permanent", "/api/v1/files/me/files/item", "permanent=true", func(c *Client) error { return c.Delete(t.Context(), "me", "files/item", true, 0) }},
		{"version", "/api/v1/files/me/files/item", "version=2", func(c *Client) error { return c.Delete(t.Context(), "me", "files/item", false, 2) }},
		{"revoke", "/api/v1/shares/share-1", "", func(c *Client) error { return c.RevokeShare(t.Context(), "share-1") }},
	} {
		for _, status := range []int{200, 202, 204, 403} {
			t.Run(fmt.Sprintf("%s/%d", operation.name, status), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodDelete || r.URL.Path != operation.path || r.URL.RawQuery != operation.query || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					w.WriteHeader(status)
					switch status {
					case 202:
						_, _ = io.WriteString(w, `{"status":"pending"}`)
					case 403:
						_, _ = io.WriteString(w, `{"error":"denied"}`)
					case 200:
						_, _ = io.WriteString(w, `{}`)
					}
				}))
				defer server.Close()
				err := operation.run(New(server.URL, "synthetic-token"))
				switch status {
				case 202:
					if err == nil || !strings.Contains(err.Error(), "completion") || !strings.Contains(err.Error(), "HTTP 202") || !strings.Contains(err.Error(), "outcome is unknown") {
						t.Errorf("accepted without completion: %v", err)
					}
				case 403:
					var apiErr *Error
					if !errors.As(err, &apiErr) || apiErr.Status != 403 || apiErr.Message != "denied" {
						t.Errorf("lost API error: %v", err)
					}
				default:
					if err != nil {
						t.Errorf("completed deletion: %v", err)
					}
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
