// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A provider added to Lux after this binary shipped must still work. Moonshot
// shipped live and `latere lux chat` refused it with `unknown provider
// "moonshot"`, forcing a CLI release for a server-side change. The CLI now
// derives the spec from what Lux publishes (route prefix + wire dialect).

func TestDeriveProviderSpecFromPublishedDialect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id, dialect, prefix string
		wantChat            string
		wantAnthropicStyle  bool
		wantEnvBase         string
	}{
		{"moonshot", "openai-chat", "/moonshot", "/moonshot/v1/chat/completions", false, "OPENAI_BASE_URL"},
		{"xai", "openai-chat", "/xai", "/xai/v1/chat/completions", false, "OPENAI_BASE_URL"},
		{"anthropic", "anthropic-messages", "/anthropic", "/anthropic/v1/messages", true, "ANTHROPIC_BASE_URL"},
		// Gemini's SDK has no bearer path, so neither chat nor env is offered.
		{"gemini", "gemini", "/gemini", "", false, ""},
		// A missing prefix falls back to /<id>.
		{"newthing", "openai-chat", "", "/newthing/v1/chat/completions", false, "OPENAI_BASE_URL"},
	}
	for _, c := range cases {
		got := deriveProviderSpec(c.id, c.dialect, c.prefix)
		if got.chatPath != c.wantChat {
			t.Errorf("%s: chatPath = %q, want %q", c.id, got.chatPath, c.wantChat)
		}
		if got.anthropicStyle != c.wantAnthropicStyle {
			t.Errorf("%s: anthropicStyle = %v, want %v", c.id, got.anthropicStyle, c.wantAnthropicStyle)
		}
		if got.envBaseVar != c.wantEnvBase {
			t.Errorf("%s: envBaseVar = %q, want %q", c.id, got.envBaseVar, c.wantEnvBase)
		}
	}
}

// TestLookupProviderResolvesUnknownFromServer is the regression proper: a
// provider absent from the built-in table is resolved from /lux/v1/providers.
func TestLookupProviderResolvesUnknownFromServer(t *testing.T) {
	t.Parallel()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lux/v1/providers" {
			http.NotFound(w, r)
			return
		}
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"id": "quasar", "dialect": "openai-chat", "default_route_prefix": "/quasar"},
		}})
	}))
	defer srv.Close()

	spec, err := lookupProviderFor(context.Background(), srv.URL, "", "tok", "quasar")
	if err != nil {
		t.Fatalf("lookupProviderFor(quasar) = %v; a provider Lux added after this "+
			"binary shipped must still resolve", err)
	}
	if spec.chatPath != "/quasar/v1/chat/completions" {
		t.Errorf("chatPath = %q", spec.chatPath)
	}
	if hits == 0 {
		t.Error("expected the live provider list to be consulted for an unknown provider")
	}
}

// A provider the binary already knows must NOT cost a round trip: the common
// path stays fast and works offline.
func TestLookupProviderKnownNeedsNoRoundTrip(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s for a locally-known provider", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := lookupProviderFor(context.Background(), srv.URL, "", "tok", "openai"); err != nil {
		t.Fatalf("openai should resolve locally: %v", err)
	}
}

// The gateway addresses models as <provider>/<model>; typing that in the CLI
// must work too, while an OpenRouter id that genuinely contains a slash is
// left intact.
func TestWireModelStripsProviderPrefix(t *testing.T) {
	t.Parallel()
	if got := localWireModel("moonshot", "moonshot/kimi-k3"); got != "kimi-k3" {
		t.Errorf("moonshot/kimi-k3 -> %q, want kimi-k3", got)
	}
	if got := localWireModel("moonshot", "kimi-k3"); got != "kimi-k3" {
		t.Errorf("bare id changed: %q", got)
	}
	if got := localWireModel("openrouter", "anthropic/claude-sonnet-4"); got != "anthropic/claude-sonnet-4" {
		t.Errorf("openrouter namespaced id must survive, got %q", got)
	}
	if got := localWireModel("local", "local/llama3"); got != "llama3" {
		t.Errorf("local prefix strip regressed: %q", got)
	}
}

// Every provider Lux routes should be reachable by name from `lux invoke`
// and `lux env`. Gemini is the documented exception (its SDK has no bearer
// path), and ollama/local are dev-loop routes. This pins that the
// openai-chat provider family is wired, since the failure mode is silent:
// the provider simply is not offered.
func TestProviderSpecsCoverTheOpenAIChatFamily(t *testing.T) {
	specs := providerSpecs()
	for _, name := range []string{"openai", "openrouter", "moonshot", "xai", "zhipu"} {
		p, ok := specs[name]
		if !ok {
			t.Errorf("provider %q is not in providerSpecs; `lux invoke %s` cannot reach it", name, name)
			continue
		}
		if p.chatPath != "/"+name+"/v1/chat/completions" {
			t.Errorf("%s chatPath = %q", name, p.chatPath)
		}
		if p.envBaseURL != "/"+name+"/v1" || p.envBaseVar != "OPENAI_BASE_URL" {
			t.Errorf("%s env = %q %q", name, p.envBaseVar, p.envBaseURL)
		}
		if p.anthropicStyle {
			t.Errorf("%s must use the OpenAI chat shape", name)
		}
	}
}
