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
		Scopes:   "read:sandbox",
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
		Scopes:    "read:sandbox",
		NoBrowser: true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runDeviceFlow err = %v, want context.Canceled", err)
	}
}

func TestExchangeForCellaTokenFallsBackToDirectExchangeOnActorAudienceMismatch(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/actor-tokens" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer auth-token" {
			t.Errorf("auth Authorization = %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","message":"invalid token: audience mismatch"}`))
	}))
	defer authSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tokens/exchange" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer auth-token" {
			t.Errorf("cella Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if label, _ := body["label"].(string); !strings.HasPrefix(label, "CLI on ") {
			t.Errorf("label = %q", label)
		}
		_, _ = w.Write([]byte(`{"access_token":"cella-token"}`))
	}))
	defer apiSrv.Close()

	got, err := exchangeForCellaToken(context.Background(), deviceFlowOpts{
		AuthURL: authSrv.URL,
		APIURL:  apiSrv.URL,
	}, "auth-token")
	if err != nil {
		t.Fatalf("exchangeForCellaToken: %v", err)
	}
	if got != "cella-token" {
		t.Fatalf("token = %q, want cella-token", got)
	}
}

func TestAuthWhoamiFallsBackToVerifiedJWTClaims(t *testing.T) {
	token := fakeJWT(t, map[string]any{
		"sub":            "user-123",
		"email":          "dev@example.com",
		"principal_type": "user",
		"org_id":         "org-456",
		"client_id":      "latere-cli",
		"scp":            []string{"read:sandbox", "write:sandbox"},
	})
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("LATERE_TOKEN_FILE", tokenPath)
	if err := api.SaveToken(tokenPath, api.Token{AccessToken: token, TokenType: "Bearer"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/tokeninfo":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","message":"invalid token: audience mismatch"}`))
		case "/v1/sandboxes":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cmd := newAuthWhoamiCmd()
	cmd.SetArgs([]string{"--api-url", srv.URL})
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
		"scopes:        read:sandbox write:sandbox",
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
