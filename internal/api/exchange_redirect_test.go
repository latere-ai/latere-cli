// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenRedirectsPreserveCallerPolicy(t *testing.T) {
	for _, operation := range []string{"actor", "exchange"} {
		for _, policy := range []string{"default", "deny"} {
			t.Run(operation+"/"+policy, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/ordinary" {
						w.Header().Set("Location", "/done")
						w.WriteHeader(http.StatusFound)
						return
					}
					if r.URL.Path != "/done" {
						w.Header().Set("Location", "/done")
						w.WriteHeader(http.StatusTemporaryRedirect)
						return
					}
					_, _ = w.Write([]byte(`{"actor_token":"actor", "access_token":"cella"}`))
				}))
				defer server.Close()
				client := server.Client()
				denied := errors.New("caller disallows redirects")
				calls := 0
				if policy == "deny" {
					client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
						calls++
						return denied
					}
				}
				var err error
				if operation == "actor" {
					_, err = MintActorToken(t.Context(), client, server.URL, "root", "sandboxd", 60)
				} else {
					_, err = ExchangeAtCella(t.Context(), client, server.URL, "actor")
				}
				if policy == "deny" {
					if !errors.Is(err, denied) || calls != 1 {
						t.Fatalf("caller policy lost: err=%v, calls=%d", err, calls)
					}
				} else if err != nil || client.CheckRedirect != nil {
					t.Fatalf("default policy changed: err=%v", err)
				}
				// The caller's own client must still apply its original policy,
				// including ordinary POST-to-GET redirects outside token requests.
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/ordinary", strings.NewReader("body"))
				if err != nil {
					t.Fatal(err)
				}
				resp, err := client.Do(req)
				if resp != nil {
					_ = resp.Body.Close()
				}
				if policy == "deny" {
					if !errors.Is(err, denied) || calls != 2 {
						t.Fatalf("caller client was mutated: err=%v, calls=%d", err, calls)
					}
				} else if err != nil {
					t.Fatalf("caller client was mutated: %v", err)
				}
			})
		}
	}
}
