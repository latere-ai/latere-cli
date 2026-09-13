// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestWhoamiHonorsOutputWriter(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	for _, fallback := range []bool{false, true} {
		for _, org := range []bool{false, true} {
			claims := map[string]any{"sub": "owner", "email": "dev@example.com", "principal_type": "user", "client_id": "latere-cli", "scopes": []string{"one", "two"}, "scp": []string{"one", "two"}}
			want := "sub:           owner\nemail:         dev@example.com\nprincipal:     user\ncontext:       personal\n"
			if org {
				claims["org_id"] = "org-123"
				want = "sub:           owner\nemail:         dev@example.com\nprincipal:     user\ncontext:       org\norg_id:        org-123\n"
			}
			want += "client_id:     latere-cli\nscopes:        one two\n"
			if err := api.SaveAuthToken(api.Token{AccessToken: fakeJWT(t, claims)}); err != nil {
				t.Fatal(err)
			}
			for _, fail := range []bool{false, true} {
				var probes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/tokeninfo":
						probes.Add(1)
						if fallback {
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						_ = json.NewEncoder(w).Encode(claims)
					default:
						t.Errorf("unexpected request: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Setenv("AUTH_URL", server.URL)
				out := &failingEnvWriter{}
				var wantErr error
				if fail {
					wantErr = errors.New("output unavailable")
					out.failAt, out.err = 1, wantErr
				}
				cmd := newAuthWhoamiCmd()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(out)
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{"--auth-url", server.URL})
				err := cmd.Execute()
				server.Close()
				if !errors.Is(err, wantErr) {
					t.Errorf("fallback=%v org=%v fail=%v: error=%v, want %v", fallback, org, fail, err, wantErr)
				}
				if out.calls != 1 {
					t.Errorf("configured writer received %d writes, want 1", out.calls)
				}
				if !fail && out.String() != want {
					t.Errorf("identity output=%q, want %q", out.String(), want)
				}
				if probes.Load() != 1 {
					t.Errorf("issuer requests=%d, want 1", probes.Load())
				}
			}
		}
	}
}
