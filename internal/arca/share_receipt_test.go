// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package arca

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testSubject = "https://auth.latere.ai|9ab3"

// A subject grant is held to the recipient it names. A receipt that
// changes any of it leaves the outcome unknown rather than reporting a
// grant nobody asked for.
func TestCreateGrantReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		valid       bool
	}{
		{"valid", "extra", true, true},
		{"missing id", "id", nil, false},
		{"blank id", "id", " \t", false},
		{"missing status", "status", nil, false},
		{"revoked", "status", "revoked", false},
		{"missing permission", "permission", nil, false},
		{"wrong permission", "permission", "manage", false},
		{"missing kind", "grantee_kind", nil, false},
		{"token kind", "grantee_kind", GranteeLink, false},
		{"missing grantee", "grantee", nil, false},
		{"wrong grantee", "grantee", "https://auth.latere.ai|other", false},
		{"missing prefix", "path_prefix", nil, false},
		{"wrong prefix", "path_prefix", "files/other", false},
		{"missing owner", "owner", nil, false},
		{"wrong owner", "owner", "https://auth.latere.ai|7c22", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"id": "share-1", "status": "active", "permission": "write",
				"grantee_kind": GranteeSubject, "grantee": "https://auth.latere.ai|7c22",
				"path_prefix": "files/item", "owner": testSubject,
			}
			if tc.value == nil {
				delete(body, tc.field)
			} else {
				body[tc.field] = tc.value
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var in CreateGrantRequest
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Owner != testSubject ||
					in.PathPrefix != "files/item" || in.Grantee != "https://auth.latere.ai|7c22" || in.Permission != "write" {
					t.Errorf("request=%+v error=%v", in, err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/shares" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			got, err := New(server.URL, "synthetic-token").CreateGrant(t.Context(), CreateGrantRequest{
				Owner: testSubject, PathPrefix: "files/item", Grantee: "https://auth.latere.ai|7c22", Permission: "write",
			})
			if tc.valid {
				if err != nil || got == nil || got.ID != "share-1" {
					t.Errorf("valid receipt: result=%+v error=%v", got, err)
				}
			} else if err == nil || got != nil || !strings.Contains(err.Error(), "share creation receipt") || !strings.Contains(err.Error(), "outcome is unknown") {
				t.Errorf("invalid receipt: result=%+v error=%v", got, err)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}

// A token grant is answered once with the token, so a receipt without one
// is a grant nobody can reach and the outcome is unknown. The token must
// not reach the error either: an error is written where a token is not.
func TestCreateLinkReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		valid       bool
	}{
		{"valid", "extra", true, true},
		{"missing id", "id", nil, false},
		{"revoked", "status", "revoked", false},
		{"above read", "permission", "write", false},
		{"missing kind", "grantee_kind", nil, false},
		{"other kind", "grantee_kind", GranteePublic, false},
		{"missing token", "token", nil, false},
		{"blank token", "token", " \t", false},
		{"missing url", "url", nil, false},
		{"blank url", "url", " \t", false},
		{"wrong prefix", "path_prefix", "files/other", false},
		{"wrong owner", "owner", "https://auth.latere.ai|7c22", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"id": "share-1", "status": "active", "permission": "read",
				"grantee_kind": GranteeLink, "path_prefix": "files/item", "owner": testSubject,
				"token": "synthetic-link-value", "url": "/v1/shares/links/synthetic-link-value",
			}
			if tc.value == nil {
				delete(body, tc.field)
			} else {
				body[tc.field] = tc.value
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in CreateLinkRequest
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Kind != GranteeLink || in.Permission != "" {
					t.Errorf("request=%+v error=%v", in, err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/shares/links" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			got, err := New(server.URL, "synthetic-token").CreateLink(t.Context(), CreateLinkRequest{
				Owner: testSubject, PathPrefix: "files/item", Kind: GranteeLink,
			})
			if tc.valid {
				if err != nil || got == nil || got.Token != "synthetic-link-value" {
					t.Errorf("valid receipt: result=%+v error=%v", got, err)
				}
				return
			}
			if err == nil || got != nil || !strings.Contains(err.Error(), "share creation receipt") ||
				strings.Contains(err.Error(), "synthetic-link-value") {
				t.Errorf("invalid receipt: result=%+v error=%v", got, err)
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
			c := New(server.URL, "synthetic-token")
			grant, err := c.CreateGrant(t.Context(), CreateGrantRequest{
				Owner: "me", PathPrefix: "files/item", Grantee: "https://auth.latere.ai|7c22", Permission: "read",
			})
			if err == nil || grant != nil {
				t.Errorf("empty grant receipt: result=%+v error=%v", grant, err)
			}
			link, err := c.CreateLink(t.Context(), CreateLinkRequest{
				Owner: "me", PathPrefix: "files/item", Kind: GranteeLink,
			})
			if err == nil || link != nil {
				t.Errorf("empty link receipt: result=%+v error=%v", link, err)
			}
		})
	}
}

// The public is a token grant of another kind, minted on the same route.
func TestCreatePublicLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in CreateLinkRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Kind != GranteePublic {
			t.Errorf("kind = %q, want %q", in.Kind, GranteePublic)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Link{
			ID: "share-2", Status: "active", Permission: "read", GranteeKind: GranteePublic,
			PathPrefix: "files/item", Owner: testSubject, Token: "public-token",
			URL: "/v1/shares/links/public-token",
		})
	}))
	defer server.Close()
	got, err := New(server.URL, "synthetic-token").CreateLink(t.Context(), CreateLinkRequest{
		Owner: testSubject, PathPrefix: "files/item", Kind: GranteePublic,
	})
	if err != nil || got == nil || got.GranteeKind != GranteePublic {
		t.Errorf("public link: result=%+v error=%v", got, err)
	}
}

// The two kinds live in two id spaces and a caller holds one id, so a
// revoke tries the subject grant and then the token grant.
func TestRevokeShareTriesBothIDSpaces(t *testing.T) {
	for _, tc := range []struct {
		name, id  string
		wantPaths []string
	}{
		{"subject grant", "grant-1", []string{"/v1/shares/grant-1"}},
		{"token grant", "link-1", []string{"/v1/shares/link-1", "/v1/shares/links/link-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, r.URL.Path)
				if r.URL.Path == "/v1/shares/link-1" {
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"Nothing here answers to that path.","details":{"request_id":"req_01J8R4"}}}`)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			if err := New(server.URL, "synthetic-token").RevokeShare(t.Context(), tc.id); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			if len(seen) != len(tc.wantPaths) {
				t.Fatalf("paths = %v, want %v", seen, tc.wantPaths)
			}
			for i, want := range tc.wantPaths {
				if seen[i] != want {
					t.Errorf("path %d = %q, want %q", i, seen[i], want)
				}
			}
		})
	}
}

// A revoke that finds neither id space reports the refusal rather than
// reporting success.
func TestRevokeShareReportsAnUnknownID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"Nothing here answers to that path.","details":{"request_id":"req_01J8R4"}}}`)
	}))
	defer server.Close()
	err := New(server.URL, "synthetic-token").RevokeShare(t.Context(), "absent")
	var derr *Error
	if !asArcaErr(err, &derr) || derr.Status != http.StatusNotFound {
		t.Errorf("revoke of an unknown id = %v", err)
	}
}

// The server stores a subtree without its trailing separator, and a person
// types one. The receipt is still the subtree that was asked for.
func TestShareReceiptAcceptsTheServersPrefixForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Grant{
			ID: "share-1", Status: "active", Permission: "read", GranteeKind: GranteeSubject,
			Grantee: "https://auth.latere.ai|7c22", PathPrefix: "files/reports", Owner: testSubject,
		})
	}))
	defer server.Close()
	got, err := New(server.URL, "synthetic-token").CreateGrant(t.Context(), CreateGrantRequest{
		Owner: testSubject, PathPrefix: "files/reports/", Grantee: "https://auth.latere.ai|7c22", Permission: "read",
	})
	if err != nil || got == nil || got.PathPrefix != "files/reports" {
		t.Errorf("trailing separator: result=%+v error=%v", got, err)
	}
}
