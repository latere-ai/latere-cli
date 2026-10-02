// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestPrintTokenHonorsOutputWriter(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	if err := api.SaveAuthToken(api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "write failure"
		}
		t.Run(name, func(t *testing.T) {
			out := &failingEnvWriter{}
			var wantErr error
			if fail {
				wantErr = errors.New("output unavailable")
				out.failAt, out.err = 1, wantErr
			}
			cmd := newAuthPrintTokenCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetOut(out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(nil)
			if err := cmd.Execute(); !errors.Is(err, wantErr) {
				t.Errorf("output error = %v, want %v", err, wantErr)
			}
			if out.calls != 1 {
				t.Errorf("configured writer received %d writes, want 1", out.calls)
			}
			if !fail && out.String() != "synthetic-token\n" {
				t.Errorf("configured output = %q", out.String())
			}
		})
	}
}

// TestPrintTokenRefreshesALapsedLogin: a saved token past its expiry is
// refreshed before it is printed, so a script gets one the issuer accepts.
func TestPrintTokenRefreshesALapsedLogin(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-token","refresh_token":"next-refresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()
	if err := api.SaveAuthToken(api.Token{AccessToken: "lapsed-token", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	out := &failingEnvWriter{}
	cmd := newAuthPrintTokenCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--auth-url", server.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "fresh-token\n" {
		t.Errorf("printed %q, want the refreshed token", out.String())
	}
}
