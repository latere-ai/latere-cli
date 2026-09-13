// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"latere.ai/x/pkg/scopes"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestRunDeviceFlowOpensVerificationURL(t *testing.T) {
	var opened string
	origOpenBrowser := openBrowser
	defer func() { openBrowser = origOpenBrowser }()

	ctx, cancel := context.WithCancel(context.Background())
	openBrowser = func(rawURL string) error {
		opened = rawURL
		cancel()
		return nil
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device/code":
			_, _ = w.Write([]byte(`{
				"device_code":"dev-1",
				"user_code":"ABCD-EFGH",
				"verification_uri":"https://auth.example/device",
				"verification_uri_complete":"https://auth.example/device?user_code=ABCD-EFGH",
				"expires_in":600,
				"interval":5
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	err := runDeviceFlow(ctx, deviceFlowOpts{
		AuthURL:  srv.URL,
		ClientID: "latere-cli",
		Scopes:   api.LoginScopes,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runDeviceFlow err = %v, want context.Canceled", err)
	}
	const want = "https://auth.example/device?user_code=ABCD-EFGH"
	if opened != want {
		t.Fatalf("opened URL = %q, want %q", opened, want)
	}
}

func TestRunDeviceFlowNoBrowserSkipsOpen(t *testing.T) {
	origOpenBrowser := openBrowser
	defer func() { openBrowser = origOpenBrowser }()

	ctx, cancel := context.WithCancel(context.Background())
	openBrowser = func(rawURL string) error {
		t.Fatalf("openBrowser called with %q", rawURL)
		return nil
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device/code":
			cancel()
			_, _ = w.Write([]byte(`{
				"device_code":"dev-1",
				"user_code":"ABCD-EFGH",
				"verification_uri":"https://auth.example/device",
				"verification_uri_complete":"https://auth.example/device?user_code=ABCD-EFGH",
				"expires_in":600,
				"interval":5
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	err := runDeviceFlow(ctx, deviceFlowOpts{
		AuthURL:   srv.URL,
		ClientID:  "latere-cli",
		Scopes:    api.LoginScopes,
		NoBrowser: true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runDeviceFlow err = %v, want context.Canceled", err)
	}
}

// A pasted login replaces whatever was on file: one credential, one
// principal. A stale login left beside it would let a later command
// attribute work to the previous account.
func TestAuthLoginTokenReplacesThePreviousLogin(t *testing.T) {
	authTokenPath := filepath.Join(t.TempDir(), "auth-token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", authTokenPath)
	if err := api.SaveAuthToken(api.Token{AccessToken: "stale-login", TokenType: "Bearer"}); err != nil {
		t.Fatalf("seed login: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokeninfo" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"u-1"}`))
	}))
	defer srv.Close()

	cmd := newAuthLoginCmd()
	cmd.SetArgs([]string{"--token", "pasted-login", "--auth-url", srv.URL, "--no-git"})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("login --token: %v", err)
	}

	got, err := api.LoadAuthToken()
	if err != nil {
		t.Fatalf("LoadAuthToken: %v", err)
	}
	if got.AccessToken != "pasted-login" {
		t.Fatalf("saved login = %q, want pasted-login", got.AccessToken)
	}
	if got.RefreshToken != "" {
		t.Error("a pasted login kept a refresh token from the previous principal")
	}
}

func TestAuthWhoamiFallsBackToTheSavedTokensClaims(t *testing.T) {
	token := fakeJWT(t, map[string]any{
		"sub":            "user-123",
		"email":          "dev@example.com",
		"principal_type": "user",
		"org_id":         "org-456",
		"client_id":      "latere-cli",
		"scp":            []string{scopes.AgentsRun.Name, scopes.AgentsRead.Name, scopes.AgentsWrite.Name},
	})
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	if err := api.SaveAuthToken(api.Token{AccessToken: token, TokenType: "Bearer"}); err != nil {
		t.Fatalf("SaveAuthToken: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/tokeninfo" {
			t.Errorf("whoami called %s; it speaks to the issuer alone", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	cmd := newAuthWhoamiCmd()
	cmd.SetArgs([]string{"--auth-url", srv.URL})
	out, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	for _, want := range []string{
		"sub:           user-123",
		"email:         dev@example.com",
		"principal:     user",
		"context:       org",
		"org_id:        org-456",
		"client_id:     latere-cli",
		"scopes:        run:agents read:agents write:agents",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func fakeJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal JWT part: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]any{"alg": "none"}) + "." + enc(payload) + ".sig"
}

func captureStdout(fn func() error) (string, error) {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)
	_ = r.Close()
	if runErr != nil {
		return buf.String(), runErr
	}
	return buf.String(), copyErr
}
