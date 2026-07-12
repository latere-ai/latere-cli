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
		{[]string{"lux", "--help"}, []string{"lux.latere.ai", "allocating an API key", "LUX_API_URL", "latere login"}},
		{[]string{"lux", "env", "--help"}, []string{"stock SDK at a Lux route", "ANTHROPIC_AUTH_TOKEN", "--ttl"}},
		{[]string{"lux", "invoke", "--help"}, []string{"diagnostic, not an assistant", "latere topos -p", "--model"}},
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
	if err == nil || !strings.Contains(err.Error(), "latere login") {
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
	if err == nil || !strings.Contains(err.Error(), "latere login") {
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
	var gotAuth string
	gotPaths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths[r.URL.Path] = true
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/lux/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "gpt-5", "model": "gpt-5-mini", "provider": "openai"}},
			})
		case "/lux/v1/rates":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"provider": "openai", "model": "gpt-5*", "input_usd_per_m": 5.0, "output_usd_per_m": 15.0},
					{"provider": "openai", "model": "gpt-5-mini", "input_usd_per_m": 1.25, "output_usd_per_m": 10.0},
				},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
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
	if !gotPaths["/lux/v1/models"] || !gotPaths["/lux/v1/rates"] {
		t.Errorf("paths hit = %v; models must join the rate card", gotPaths)
	}
	if gotAuth != "Bearer "+tok {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(out, "gpt-5") {
		t.Errorf("output missing model:\n%s", out)
	}
	// The exact-match rate (1.25) must win over the gpt-5* pattern (5.0).
	if !strings.Contains(out, "1.25") {
		t.Errorf("output missing joined rate:\n%s", out)
	}
}

func TestRateForPicksMostSpecific(t *testing.T) {
	rates := []map[string]any{
		{"provider": "openai", "model": "gpt-5*", "input_usd_per_m": 5.0},
		{"provider": "openai", "model": "gpt-5-mini*", "input_usd_per_m": 1.25},
		{"provider": "anthropic", "model": "gpt-5-mini*", "input_usd_per_m": 99.0},
		{"provider": "openai", "model": "gpt-5-mini-2026", "input_usd_per_m": 1.0},
	}
	if r := rateFor(rates, "openai", "gpt-5-mini-2026"); r == nil || r["input_usd_per_m"] != 1.0 {
		t.Errorf("exact match: got %v", r)
	}
	if r := rateFor(rates, "openai", "gpt-5-mini-x"); r == nil || r["input_usd_per_m"] != 1.25 {
		t.Errorf("longest pattern: got %v", r)
	}
	if r := rateFor(rates, "openai", "gpt-5-turbo"); r == nil || r["input_usd_per_m"] != 5.0 {
		t.Errorf("shorter pattern: got %v", r)
	}
	if r := rateFor(rates, "gemini", "gpt-5-mini"); r != nil {
		t.Errorf("provider mismatch: got %v", r)
	}
}

func TestLuxRatesHiddenAlias(t *testing.T) {
	for _, c := range newLuxCmd().Commands() {
		if c.Name() == "rates" {
			if !c.Hidden {
				t.Error("lux rates must be hidden (rates ride lux models)")
			}
			return
		}
	}
	t.Fatal("lux rates alias missing")
}

func TestLuxInvokeChatAlias(t *testing.T) {
	root := NewRoot("test")
	cmd, _, err := root.Find([]string{"lux", "chat"})
	if err != nil || cmd.Name() != "invoke" {
		t.Fatalf("lux chat must resolve to invoke; got %v, %v", cmd, err)
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
		if r.URL.Path == "/lux/v1/models" { // provider inference lookup
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"model": "gpt-4o-mini", "provider": "openai"},
			}})
			return
		}
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
		root.SetArgs([]string{"lux", "invoke", "--lux-url", srv.URL, "--token", tok,
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

func TestLuxProvidersHidden(t *testing.T) {
	for _, c := range newLuxCmd().Commands() {
		if c.Name() == "providers" {
			if !c.Hidden {
				t.Error("lux providers must be hidden (provider rides each lux models row)")
			}
			return
		}
	}
	t.Fatal("lux providers alias missing")
}

func TestLuxUsageOverview(t *testing.T) {
	gotParams := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/lux/v1/usage":
			gotParams["group_by"] = q.Get("group_by")
			if q.Get("from") == "" || q.Get("to") == "" {
				t.Error("usage missing from/to range")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"group": "claude-fable-5", "calls": 20, "tokens_in": 1200000, "tokens_out": 340000, "cost_usd_micro": 280000},
				{"group": "gpt-5-mini", "calls": 10, "tokens_in": 500, "tokens_out": 100, "cost_usd_micro": 3400},
			}})
		case "/lux/v1/usage/series":
			gotParams["interval"] = q.Get("interval")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"ts": "2026-07-10T00:00:00Z", "group": "claude-fable-5", "calls": 12, "cost_usd_micro": 200000},
				{"ts": "2026-07-10T00:00:00Z", "group": "gpt-5-mini", "calls": 6, "cost_usd_micro": 3400},
				{"ts": "2026-07-11T00:00:00Z", "group": "claude-fable-5", "calls": 8, "cost_usd_micro": 80000},
			}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.read"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "usage", "--lux-url", srv.URL, "--token", tok})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotParams["group_by"] != "model" || gotParams["interval"] != "day" {
		t.Errorf("params = %v (defaults: model/day for --period month)", gotParams)
	}
	for _, want := range []string{
		"$0.28",          // total... per-group cost for fable
		"30 calls",       // period total calls
		"claude-fable-5", // breakdown row
		"1.2M in",        // token formatting
		"$0.0034",        // sub-cent cost keeps precision
		"Jul 10",         // series bucket label
		"▇",              // bar chart present
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Jul 10 bucket sums both groups (203400); Jul 11 is smaller (80000):
	// the max-cost bar must belong to Jul 10.
	jul10 := strings.Index(out, "Jul 10")
	jul11 := strings.Index(out, "Jul 11")
	if jul10 == -1 || jul11 == -1 || jul10 > jul11 {
		t.Errorf("series buckets out of order:\n%s", out)
	}
}

func TestLuxUsagePeriodValidation(t *testing.T) {
	root := NewRoot("test")
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"lux", "usage", "--period", "fortnight"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "day, week, month, quarter, year") {
		t.Fatalf("want period validation error, got %v", err)
	}
}

func TestLuxUsageQuarterUsesWeekInterval(t *testing.T) {
	var interval string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lux/v1/usage/series" {
			interval = r.URL.Query().Get("interval")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.read"}})
	_, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "usage", "--period", "quarter", "--lux-url", srv.URL, "--token", tok})
		return root.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	if interval != "week" {
		t.Errorf("quarter interval = %q, want week", interval)
	}
}

func TestFmtHelpers(t *testing.T) {
	if got := fmtUSDMicro(280000); got != "$0.28" {
		t.Errorf("fmtUSDMicro(280000) = %q", got)
	}
	if got := fmtUSDMicro(3400); got != "$0.0034" {
		t.Errorf("fmtUSDMicro(3400) = %q", got)
	}
	if got := fmtUSDMicro(0); got != "$0.00" {
		t.Errorf("fmtUSDMicro(0) = %q", got)
	}
	if got := fmtTokens(1200000); got != "1.2M" {
		t.Errorf("fmtTokens = %q", got)
	}
	if got := fmtTokens(1500); got != "1.5K" {
		t.Errorf("fmtTokens = %q", got)
	}
	if got := fmtTokens(42); got != "42" {
		t.Errorf("fmtTokens = %q", got)
	}
}

func TestLuxEnvPositionalRoute(t *testing.T) {
	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	cases := []struct {
		route    string
		wantBase string
		wantKey  string
	}{
		{"openrouter", "export OPENAI_BASE_URL=https://lux.example/openrouter/v1", "export OPENAI_API_KEY="},
		{"local", "export OPENAI_BASE_URL=https://lux.example/local/v1", "export OPENAI_API_KEY="},
		{"anthropic", "export ANTHROPIC_BASE_URL=https://lux.example/anthropic", "export ANTHROPIC_AUTH_TOKEN="},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			out, err := captureStdout(func() error {
				root := NewRoot("test")
				root.SetErr(&strings.Builder{})
				root.SetArgs([]string{"lux", "env", tc.route, "--lux-url", "https://lux.example", "--token", tok})
				return root.Execute()
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.wantBase) || !strings.Contains(out, tc.wantKey+tok) {
				t.Errorf("route %s exports wrong:\n%s", tc.route, out)
			}
		})
	}
}

func TestLuxEnvProvenanceOnStderrOnly(t *testing.T) {
	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	var errBuf strings.Builder
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&errBuf)
		root.SetArgs([]string{"lux", "env", "--lux-url", "https://lux.example", "--token", tok})
		return root.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "#") {
		t.Errorf("stdout must stay eval-clean, got:\n%s", out)
	}
	if !strings.Contains(errBuf.String(), "passthrough token") {
		t.Errorf("stderr missing provenance note: %q", errBuf.String())
	}
}

func TestLuxEnvRaw(t *testing.T) {
	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "env", "--raw", "--token", tok})
		return root.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != tok {
		t.Errorf("--raw must print the bare token, got %q", out)
	}
}

func TestLuxEnvTTLMintsActorToken(t *testing.T) {
	isolateBearer(t)
	var gotTTL float64
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/actor-tokens" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTTL, _ = body["ttl_seconds"].(float64)
		_ = json.NewEncoder(w).Encode(map[string]any{"actor_token": "short-lived"})
	}))
	defer authSrv.Close()
	writeAuthTokenFile(t, fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}}), "r", time.Now().Add(time.Hour))

	var errBuf strings.Builder
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&errBuf)
		root.SetArgs([]string{"lux", "env", "--ttl", "1h", "--lux-url", "https://lux.example", "--auth-url", authSrv.URL})
		return root.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotTTL != 3600 {
		t.Errorf("ttl_seconds = %v, want 3600", gotTTL)
	}
	if !strings.Contains(out, "export OPENAI_API_KEY=short-lived") {
		t.Errorf("exports must embed the actor token:\n%s", out)
	}
	if !strings.Contains(errBuf.String(), "actor token, expires in 1h") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestLuxEnvIdentityExpiryNote(t *testing.T) {
	isolateBearer(t)
	exp := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	writeAuthTokenFile(t, fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}}), "r", exp)

	var errBuf strings.Builder
	_, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&errBuf)
		root.SetArgs([]string{"lux", "env", "--lux-url", "https://lux.example"})
		return root.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "identity token, expires 2026-07-13T08:00Z") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestLuxTokenHiddenAlias(t *testing.T) {
	for _, c := range newLuxCmd().Commands() {
		if c.Name() == "token" {
			if !c.Hidden {
				t.Error("lux token must be hidden (env --raw owns bare-token output)")
			}
			return
		}
	}
	t.Fatal("lux token alias missing")
}

func TestLuxServeFriendlyWhenRuntimeDown(t *testing.T) {
	root := NewRoot("test")
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	// A closed port stands in for "ollama is not running".
	root.SetArgs([]string{"lux", "serve", "--upstream", "http://127.0.0.1:1"})
	err := root.Execute()
	if err == nil {
		t.Fatal("want availability error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no ollama runtime is answering at http://127.0.0.1:1") {
		t.Errorf("missing availability headline: %q", msg)
	}
	if !strings.Contains(msg, "ollama serve") || !strings.Contains(msg, "--upstream") {
		t.Errorf("missing actionable hints: %q", msg)
	}
	if strings.Contains(msg, "dial tcp") {
		t.Errorf("dial noise leaked into the message: %q", msg)
	}
}

func TestLuxInvokeInfersProviderFromCatalog(t *testing.T) {
	var invokedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lux/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"model": "claude-sonnet-5", "provider": "anthropic"},
				{"model": "anthropic/claude-sonnet-5", "provider": "openrouter"},
			}})
		case "/anthropic/v1/messages":
			invokedPath = r.URL.Path
			if r.Header.Get("anthropic-version") == "" {
				t.Error("missing anthropic-version header")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "hi"}},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.read", "llm.invoke"}})
	out, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "invoke", "--lux-url", srv.URL, "--token", tok, "--model", "claude-sonnet-5", "Say hi"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if invokedPath != "/anthropic/v1/messages" {
		t.Errorf("model routed to %q, want the anthropic route", invokedPath)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("out = %q", out)
	}
}

func TestLuxInvokeUnknownModelPointsAtCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"model": "gpt-4o", "provider": "openai"},
		}})
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.read", "llm.invoke"}})
	root := NewRoot("test")
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"lux", "invoke", "--lux-url", srv.URL, "--token", tok, "--model", "nope-1", "Say hi"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "latere lux models") {
		t.Fatalf("want catalog hint, got %v", err)
	}
}

func TestLuxInvokeExplicitProviderSkipsInference(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "hi"}}},
		})
	}))
	defer srv.Close()

	tok := fakeJWT(t, map[string]any{"sub": "u", "scp": []string{"llm.invoke"}})
	_, err := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"lux", "invoke", "--lux-url", srv.URL, "--token", tok, "--provider", "openai", "--model", "gpt-4o", "Say hi"})
		return root.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if p == "/lux/v1/models" {
			t.Error("explicit --provider must not fetch the catalog")
		}
	}
}

func TestFmtRateRoundsFloatArtifacts(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.024999999999999998, "0.025"},
		{1.25, "1.25"},
		{15, "15"},
		{0.5, "0.5"},
		{0, "0"},
	}
	for _, tc := range cases {
		if got := fmtRate(tc.in); got != tc.want {
			t.Errorf("fmtRate(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
