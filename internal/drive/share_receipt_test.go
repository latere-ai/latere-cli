// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package drive

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCreateShareReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		valid       bool
	}{
		{"valid", "extra", true, true},
		{"pending", "status", "pending", true},
		{"existing", "existing", true, true},
		{"missing id", "id", nil, false},
		{"blank id", "id", " \t", false},
		{"missing status", "status", nil, false},
		{"revoked", "status", "revoked", false},
		{"missing permission", "permission", nil, false},
		{"wrong permission", "permission", "manage", false},
		{"missing grantee", "grantee_type", nil, false},
		{"wrong grantee", "grantee_type", "public", false},
		{"missing prefix", "path_prefix", nil, false},
		{"wrong prefix", "path_prefix", "files/other", false},
		{"missing owner", "owner", nil, false},
		{"wrong owner", "owner", "o-other", false},
		{"missing url", "url", nil, false},
		{"blank url", "url", " \t", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"id": "share-1", "status": "active", "permission": "read", "grantee_type": "link", "path_prefix": "files/item", "owner": "o-example", "url": "/s/synthetic-link"}
			if tc.value == nil {
				delete(body, tc.field)
			} else {
				body[tc.field] = tc.value
			}
			if tc.name == "pending" {
				delete(body, "url")
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var in CreateShareRequest
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Owner != "o-example" || in.PathPrefix != "files/item" || in.GranteeType != "link" || in.Permission != "read" {
					t.Errorf("request=%+v error=%v", in, err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/shares" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			got, err := New(server.URL, "synthetic-token").CreateShare(t.Context(), CreateShareRequest{Owner: "o-example", PathPrefix: "files/item", GranteeType: "link", Permission: "read"})
			if tc.valid {
				if err != nil || got == nil || got.ID != "share-1" {
					t.Errorf("valid receipt: result=%+v error=%v", got, err)
				}
			} else if err == nil || got != nil || !strings.Contains(err.Error(), "share creation receipt") || !strings.Contains(err.Error(), "outcome is unknown") || strings.Contains(err.Error(), "synthetic-link") {
				t.Errorf("invalid receipt: result=%+v error=%v", got, err)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}

func TestCreateShareEmptyReceipt(t *testing.T) {
	for _, body := range []string{"null", "{}"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			got, err := New(server.URL, "synthetic-token").CreateShare(t.Context(), CreateShareRequest{Owner: "me", PathPrefix: "files/item", GranteeType: "principal", GranteeID: "u-other", Permission: "read"})
			if err == nil || got != nil {
				t.Errorf("empty receipt: result=%+v error=%v", got, err)
			}
		})
	}
}

func TestCreateShareReceiptGrantModes(t *testing.T) {
	for _, tc := range []struct {
		name, grantee, owner, resolved, status, url string
		existing, valid                             bool
	}{
		{"person", "principal", "me", "u-example", "active", "", false, true},
		{"org", "org", "org", "o-example", "active", "", false, true},
		{"team", "team", "u-example", "u-example", "active", "", false, true},
		{"role", "role", "org", "o-example", "active", "", false, true},
		{"email", "email", "me", "u-example", "active", "/s/token", false, true},
		{"email missing token", "email", "me", "u-example", "active", "", false, false},
		{"email existing", "email", "me", "u-example", "active", "", true, true},
		{"email pending", "email", "org", "o-example", "pending", "", false, true},
		{"public", "public", "me", "u-example", "active", "/s/token", false, true},
		{"public missing token", "public", "me", "u-example", "active", "", false, false},
		{"public existing", "public", "me", "u-example", "active", "", true, true},
		{"public pending", "public", "org", "o-example", "pending", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(ShareCreated{ID: "share-1", Status: tc.status, Permission: "read", GranteeType: tc.grantee, PathPrefix: "files/item", Owner: tc.resolved, URL: tc.url, Existing: tc.existing})
			}))
			defer server.Close()
			got, err := New(server.URL, "synthetic-token").CreateShare(t.Context(), CreateShareRequest{Owner: tc.owner, PathPrefix: "files/item", GranteeType: tc.grantee, Permission: "read"})
			if tc.valid {
				if err != nil || got == nil || got.URL != tc.url || got.Existing != tc.existing {
					t.Errorf("valid grant: result=%+v error=%v", got, err)
				}
			} else if err == nil || got != nil {
				t.Errorf("missing token: result=%+v error=%v", got, err)
			}
		})
	}
}
