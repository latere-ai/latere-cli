package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- helpers ----
// fakeJWT (auth_test.go) builds an unsigned three-part JWT; the CLI only
// base64-decodes the payload for scope preflight, so a fake signature is
// fine for client-side tests.

func TestBearerHasOrg(t *testing.T) {
	withOrg := fakeJWT(t, map[string]any{"sub": "u", "org_id": "org-123"})
	if !bearerHasOrg(withOrg) {
		t.Error("bearerHasOrg(JWT with org_id) = false, want true")
	}
	withoutOrg := fakeJWT(t, map[string]any{"sub": "u"})
	if bearerHasOrg(withoutOrg) {
		t.Error("bearerHasOrg(JWT without org_id) = true, want false")
	}
	blankOrg := fakeJWT(t, map[string]any{"sub": "u", "org_id": "  "})
	if bearerHasOrg(blankOrg) {
		t.Error("bearerHasOrg(JWT with blank org_id) = true, want false")
	}
	if bearerHasOrg("not-a-jwt") {
		t.Error("bearerHasOrg(non-JWT) = true, want false")
	}
}

// isolateBearer clears the ambient bearer sources (env + sandbox file)
// so luxBearer's resolution is deterministic in tests.
func isolateBearer(t *testing.T) {
	t.Helper()
	t.Setenv("LATERE_LUX_TOKEN", "")
	old := sandboxTokenFile
	sandboxTokenFile = filepath.Join(t.TempDir(), "no-sandbox-token")
	t.Cleanup(func() { sandboxTokenFile = old })
}

func writeAuthTokenFile(t *testing.T, access, refresh string, expiresAt time.Time) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth-token.json")
	b, _ := json.Marshal(map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_at":    expiresAt,
	})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_AUTH_TOKEN_FILE", p)
}

// ---- wiring ----

func TestLuxCommandRegisteredInRoot(t *testing.T) {
	var found bool
	for _, c := range NewRoot("test").Commands() {
		if c.Name() == "lux" {
			found = true
		}
	}
	if !found {
		t.Fatal("'lux' command not registered in root")
	}
}

func TestLuxHelpText(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"lux", "--help"}, []string{"lux.latere.ai", "allocating an API key", "LUX_API_URL", "latere auth login"}},
		{[]string{"lux", "env", "--help"}, []string{"provider SDK at Lux", "ANTHROPIC_AUTH_TOKEN"}},
		{[]string{"lux", "chat", "--help"}, []string{"one-shot prompt", "--model"}},
		{[]string{"lux", "access", "set", "--help"}, []string{"provider key", "llm.invoke"}},
	}
	for _, tc := range cases {
		got, err := executeForHelp(NewRoot("test"), tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%v help missing %q\n%s", tc.args, w, got)
			}
		}
	}
}

func TestResolveLuxURL(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("LUX_API_URL", "http://env")
		if got := resolveLuxURL("http://flag"); got != "http://flag" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("env over default", func(t *testing.T) {
		t.Setenv("LUX_API_URL", "http://env")
		if got := resolveLuxURL(""); got != "http://env" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("default", func(t *testing.T) {
		t.Setenv("LUX_API_URL", "")
		if got := resolveLuxURL(""); got != "https://lux.latere.ai" {
			t.Errorf("got %q", got)
		}
	})
}

// ---- bearer resolution ----

func TestLuxBearerPassthroughPrecedence(t *testing.T) {
	t.Run("flag wins over everything", func(t *testing.T) {
		isolateBearer(t)
		t.Setenv("LATERE_LUX_TOKEN", "env-token")
		got, err := luxBearer(t.Context(), "flag-token", "", "")
		if err != nil || got != "flag-token" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
	t.Run("env over sandbox/mint", func(t *testing.T) {
		isolateBearer(t)
		t.Setenv("LATERE_LUX_TOKEN", "env-token")
		got, err := luxBearer(t.Context(), "", "", "")
		if err != nil || got != "env-token" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
	t.Run("sandbox file over mint", func(t *testing.T) {
		isolateBearer(t)
		f := filepath.Join(t.TempDir(), "sbtok")
		if err := os.WriteFile(f, []byte("  sandbox-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sandboxTokenFile = f
		got, err := luxBearer(t.Context(), "", "", "")
		if err != nil || got != "sandbox-token" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
}

func TestLuxBearerNoAuthToken(t *testing.T) {
	isolateBearer(t)
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "absent.json"))
	_, err := luxBearer(t.Context(), "", "", "")
	if err == nil || !strings.Contains(err.Error(), "latere auth login") {
		t.Fatalf("want re-login error, got %v", err)
	}
}

func TestLuxBearerMintsActorToken(t *testing.T) {
	isolateBearer(t)
	var gotAuth, gotBody string
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/actor-tokens" {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := json.Marshal(map[string]any{})
		_ = b
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotBody, _ = body["audience"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"actor_token": "minted-actor", "expires_in": 300})
	}))
	defer authSrv.Close()

	// Future expiry → no refresh, straight to mint.
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))

	got, err := luxBearer(t.Context(), "", "", authSrv.URL)
	if err != nil {
		t.Fatalf("luxBearer: %v", err)
	}
	if got != "minted-actor" {
		t.Errorf("bearer = %q, want minted-actor", got)
	}
	if gotAuth != "Bearer access-root" {
		t.Errorf("mint Authorization = %q", gotAuth)
	}
	if gotBody != "lux.latere.ai" {
		t.Errorf("mint audience = %q, want lux.latere.ai", gotBody)
	}
}

func TestLuxBearerRefreshesThenMints(t *testing.T) {
	isolateBearer(t)
	var refreshed bool
	var mintBearer string
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token": // oauth2 refresh
			refreshed = true
			_ = r.ParseForm()
			if r.FormValue("grant_type") != "refresh_token" {
				http.Error(w, "bad grant", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-new", "token_type": "Bearer",
				"refresh_token": "refresh-new", "expires_in": 3600,
			})
		case "/actor-tokens":
			mintBearer = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"actor_token": "minted-after-refresh"})
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer authSrv.Close()

	// Past expiry → refresh first.
	writeAuthTokenFile(t, "access-old", "refresh-old", time.Now().Add(-time.Hour))

	got, err := luxBearer(t.Context(), "", "", authSrv.URL)
	if err != nil {
		t.Fatalf("luxBearer: %v", err)
	}
	if !refreshed {
		t.Error("expected a refresh call to /token")
	}
	if got != "minted-after-refresh" {
		t.Errorf("bearer = %q", got)
	}
	if mintBearer != "Bearer access-new" {
		t.Errorf("mint used %q, want refreshed access-new", mintBearer)
	}
}

func TestLuxIdentityBearerReturnsRootToken(t *testing.T) {
	isolateBearer(t)
	// A server that fails any call proves env/token do NOT mint an actor
	// token: the identity bearer is the root token itself.
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("identity bearer must not call auth (%s); it returns the root token", r.URL.Path)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer authSrv.Close()
	writeAuthTokenFile(t, "root-access", "root-refresh", time.Now().Add(time.Hour))

	got, err := luxIdentityBearer(t.Context(), "", "", authSrv.URL)
	if err != nil {
		t.Fatalf("luxIdentityBearer: %v", err)
	}
	if got != "root-access" {
		t.Errorf("identity bearer = %q, want the root access token", got)
	}
}

func TestLuxIdentityBearerRefreshesWhenExpired(t *testing.T) {
	isolateBearer(t)
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Errorf("unexpected call %s (should refresh only, not mint)", r.URL.Path)
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "root-new", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer authSrv.Close()
	writeAuthTokenFile(t, "root-old", "root-refresh", time.Now().Add(-time.Hour))

	got, err := luxIdentityBearer(t.Context(), "", "", authSrv.URL)
	if err != nil {
		t.Fatalf("luxIdentityBearer: %v", err)
	}
	if got != "root-new" {
		t.Errorf("identity bearer = %q, want refreshed root-new", got)
	}
}

// ---- scope preflight ----

func TestEnsureLuxScope(t *testing.T) {
	read := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.read"}})
	if err := ensureLuxScope(read, []string{"llm.read", "llm.invoke"}, "list models"); err != nil {
		t.Errorf("llm.read token should pass: %v", err)
	}
	admin := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.admin"}})
	if err := ensureLuxScope(admin, []string{"llm.invoke"}, "set profile"); err != nil {
		t.Errorf("llm.admin should satisfy any scope: %v", err)
	}
	none := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"read:sandbox"}})
	err := ensureLuxScope(none, []string{"llm.read", "llm.invoke"}, "list models")
	if err == nil || !strings.Contains(err.Error(), "latere auth login") {
		t.Errorf("missing scope should give re-login hint, got %v", err)
	}
	sandbox := fakeJWT(t, map[string]any{"sub": "u", "kind": "sandbox", "scp": []string{"trust-plane:egress"}})
	err = ensureLuxScope(sandbox, []string{"llm.read"}, "list models")
	if err == nil || !strings.Contains(err.Error(), "sandbox/service identity") {
		t.Errorf("sandbox token should get tailored message, got %v", err)
	}
	// Opaque (non-JWT) token: skip preflight, defer to server.
	if err := ensureLuxScope("opaque-token", []string{"llm.read"}, "list models"); err != nil {
		t.Errorf("opaque token should skip preflight: %v", err)
	}
}

// ---- discovery ----

func TestLuxModelsCallsEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "gpt-5", "provider": "openai"}},
		})
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.read"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "models", "--lux-url", srv.URL, "--token", tok})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/lux/v1/models" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer "+tok {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(out, "gpt-5") {
		t.Errorf("output missing model:\n%s", out)
	}
}

func TestLuxModelsScopePreflightBlocks(t *testing.T) {
	// Server should never be hit; preflight fails first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when preflight fails")
	}))
	defer srv.Close()
	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"read:sandbox"}})
	root := NewRoot("test")
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"lux", "models", "--lux-url", srv.URL, "--token", tok})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "llm.read") {
		t.Fatalf("want scope error, got %v", err)
	}
}

// ---- chat ----

func TestLuxChatOpenAI(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "hello there"}}},
		})
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "chat", "--lux-url", srv.URL, "--token", tok, "--model", "gpt-4o-mini", "Say hi"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/openai/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["model"] != "gpt-4o-mini" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if !strings.Contains(out, "hello there") {
		t.Errorf("output missing reply:\n%s", out)
	}
}

func TestLuxChatAnthropic(t *testing.T) {
	var gotPath, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "hi from claude"}},
		})
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "chat", "--lux-url", srv.URL, "--token", tok,
			"--provider", "anthropic", "--model", "claude-sonnet-4-6", "Say hi"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/anthropic/v1/messages" {
		t.Errorf("path = %q", gotPath)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q", gotVersion)
	}
	if _, ok := gotBody["max_tokens"]; !ok {
		t.Errorf("anthropic body missing max_tokens: %v", gotBody)
	}
	if !strings.Contains(out, "hi from claude") {
		t.Errorf("output missing reply:\n%s", out)
	}
}

// ---- env ----

func TestLuxEnvOpenAI(t *testing.T) {
	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "env", "--provider", "openai", "--lux-url", "https://lux.example", "--token", tok})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "export OPENAI_BASE_URL=https://lux.example/openai/v1") {
		t.Errorf("missing base export:\n%s", out)
	}
	if !strings.Contains(out, "export OPENAI_API_KEY="+tok) {
		t.Errorf("missing key export:\n%s", out)
	}
}

func TestLuxEnvAnthropicUsesAuthToken(t *testing.T) {
	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "env", "--provider", "anthropic", "--lux-url", "https://lux.example", "--token", tok})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "export ANTHROPIC_BASE_URL=https://lux.example/anthropic") {
		t.Errorf("missing anthropic base:\n%s", out)
	}
	if !strings.Contains(out, "export ANTHROPIC_AUTH_TOKEN="+tok) {
		t.Errorf("anthropic must use ANTHROPIC_AUTH_TOKEN (bearer), got:\n%s", out)
	}
}

func TestLuxEnvGeminiUnsupported(t *testing.T) {
	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	root := NewRoot("test")
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"lux", "env", "--provider", "gemini", "--token", tok})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "bearer path") {
		t.Fatalf("gemini env should be unsupported, got %v", err)
	}
}

// ---- access set ----

func TestLuxAccessSetPatchesBindings(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody struct {
		Bindings json.RawMessage `json:"bindings"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	_, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "access", "set", "--lux-url", srv.URL, "--token", tok,
			"--model", "gpt-5", "--provider", "openai", "--provider-key", "pk-123"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/lux/v1/me/profile" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	// bindings.models["gpt-5"].targets[0].provider_key_id == pk-123
	var bindings struct {
		Models map[string]struct {
			Targets []struct {
				ProviderKeyID string `json:"provider_key_id"`
				Provider      string `json:"provider"`
			} `json:"targets"`
		} `json:"models"`
	}
	if err := json.Unmarshal(gotBody.Bindings, &bindings); err != nil {
		t.Fatalf("bindings not valid JSON: %v (%s)", err, gotBody.Bindings)
	}
	m, ok := bindings.Models["gpt-5"]
	if !ok || len(m.Targets) != 1 || m.Targets[0].ProviderKeyID != "pk-123" || m.Targets[0].Provider != "openai" {
		t.Fatalf("unexpected bindings: %+v", bindings)
	}
}
