// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestLoginVerificationDoesNotRefreshCandidate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "auth-token.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "old-cella"}); err != nil {
		t.Fatal(err)
	}
	if err := api.SaveAuthToken(api.Token{AccessToken: "old-auth"}); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/sandboxes":
			if r.Header.Get("Authorization") == "Bearer refreshed-token" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"invalid_token"}`))
		case "/actor-tokens":
			refreshes.Add(1)
			_, _ = w.Write([]byte(`{"actor_token":"test-actor"}`))
		case "/v1/tokens/exchange":
			_, _ = w.Write([]byte(`{"access_token":"refreshed-token"}`))
		default:
			t.Error("unexpected request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("AUTH_URL", server.URL)
	if err := saveAndVerify(t.Context(), server.URL, "rejected-candidate"); err == nil {
		t.Error("candidate rejection was masked by credential refresh")
	}
	if refreshes.Load() != 0 {
		t.Error("login refreshed a different credential instead of validating the candidate")
	}
	if got, err := api.LoadToken(""); err != nil || got.AccessToken != "old-cella" {
		t.Errorf("failed verification changed saved token: %v", err)
	}
}

func TestPastedLoginSaveFailureKeepsAuthToken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", root) // Saving cannot replace a directory.
	authPath := filepath.Join(root, "auth-token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
	if err := api.SaveAuthToken(api.Token{AccessToken: "old-auth"}); err != nil {
		t.Fatal(err)
	}
	server := fakeSandboxAPI(t, true)
	if err := runAuthLogin(t, "--token", "candidate", "--api-url", server.URL, "--no-git"); err == nil {
		t.Fatal("save failure reported success")
	}
	if got, err := api.LoadAuthToken(); err != nil || got.AccessToken != "old-auth" {
		t.Errorf("save failure removed auth token: %v", err)
	}
}

func TestLoginCancellationPreservesSavedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("LATERE_TOKEN_FILE", path)
	before := `{"access_token":"old-cella"}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := saveAndVerify(ctx, "http://127.0.0.1:1", "candidate"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context cancellation", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != before {
		t.Errorf("cancelled login changed saved token: %v", err)
	}
}
