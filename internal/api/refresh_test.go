// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshAuthTokenReportsPersistenceFailure(t *testing.T) {
	for _, mode := range []string{"blocked parent", "directory target"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "auth-token.json")
			if mode == "blocked parent" {
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "auth-token.json")
			} else if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LATERE_AUTH_TOKEN_FILE", path)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
			}))
			defer server.Close()
			got, err := RefreshAuthToken(t.Context(), server.URL, Token{RefreshToken: "old-refresh"})
			_, pathErr := errors.AsType[*os.PathError](err)
			_, linkErr := errors.AsType[*os.LinkError](err)
			if (!pathErr && !linkErr) || !strings.Contains(err.Error(), "save refreshed login token") {
				t.Fatalf("save failure lost: %v", err)
			}
			if got != (Token{}) || calls != 1 {
				t.Errorf("unsaved credential returned or refresh retried: token=%+v calls=%d", got, calls)
			}
		})
	}
}

func TestRefreshAuthTokenPersistsAndPreservesRefreshToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q", got)
		}
		// Omit refresh_token in the reply: the previous one must be kept.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access", "token_type": "Bearer", "expires_in": 3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := RefreshAuthToken(context.Background(), srv.URL, Token{RefreshToken: "old-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "old-refresh" {
		t.Errorf("refreshed = %+v; want new-access with preserved old-refresh", got)
	}
	saved, err := LoadAuthToken()
	if err != nil || saved.AccessToken != "new-access" {
		t.Errorf("persisted auth token = %+v, %v", saved, err)
	}
}

func TestRefreshAuthTokenErrorPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(dir, "auth-token.json"))

	if _, err := RefreshAuthToken(context.Background(), "", Token{RefreshToken: "r"}); err == nil {
		t.Error("empty auth base: want error")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := RefreshAuthToken(context.Background(), srv.URL, Token{RefreshToken: "r"}); err == nil {
		t.Error("500 from /token: want error")
	}
}

func TestRefreshAuthTokenUsesIssuingClient(t *testing.T) {
	for _, tc := range []struct {
		name, saved, env, want string
	}{
		{"legacy default", "", "", "latere-cli"},
		{"legacy environment", "", "legacy-cli", "legacy-cli"},
		{"saved custom client", "custom-cli", "", "custom-cli"},
		{"saved client overrides environment", "custom-cli", "unrelated-cli", "custom-cli"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "auth-token.json"))
			t.Setenv("XDG_CONFIG_HOME", root)
			t.Setenv("AUTH_CLIENT_ID", tc.env)
			previous := Token{AccessToken: "old-root", RefreshToken: "old-refresh", ClientID: tc.saved}
			if err := SaveAuthToken(previous); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/token" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				if got := r.PostForm.Get("client_id"); got != tc.want {
					t.Errorf("client_id = %q, want %q", got, tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"new-root","token_type":"Bearer","expires_in":3600}`))
			}))
			defer server.Close()
			got, err := RefreshAuthToken(t.Context(), server.URL, previous)
			if err != nil {
				t.Fatal(err)
			}
			if got.ClientID != tc.want || got.RefreshToken != previous.RefreshToken || got.AccessToken != "new-root" {
				t.Errorf("refresh did not retain the client and refresh grant: %+v", got)
			}
			if saved, err := LoadAuthToken(); err != nil || saved.ClientID != got.ClientID || saved.AccessToken != got.AccessToken || saved.RefreshToken != got.RefreshToken {
				t.Errorf("refreshed credential not persisted: %v", err)
			}
		})
	}
}
