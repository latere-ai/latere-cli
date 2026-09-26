// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestResolveCellaURL(t *testing.T) {
	t.Setenv("LATERE_CELLA_URL", "")
	if got := resolveCellaURL(""); got != "https://api.latere.ai/v1/environments" {
		t.Errorf("default = %q, want the hosted control plane under the platform origin", got)
	}
	t.Setenv("LATERE_CELLA_URL", "http://localhost:8080/v1/environments/")
	if got := resolveCellaURL(""); got != "http://localhost:8080/v1/environments" {
		t.Errorf("env = %q", got)
	}
	if got := resolveCellaURL("https://cella.example.com/"); got != "https://cella.example.com" {
		t.Errorf("flag = %q, want the flag over the environment", got)
	}
}

// The issuer is inferred from the default base URL's host.
func TestCellaIssuerFromDefaultURL(t *testing.T) {
	t.Setenv("AUTH_URL", "")
	if got := api.ResolveAuthURL(defaultCellaURL, ""); got != "https://auth.latere.ai" {
		t.Errorf("issuer = %q, want https://auth.latere.ai", got)
	}
}

// A removed command of the retired API names why it is gone, rather than
// printing the group's help and exiting 0.
func TestRemovedCellaCommands(t *testing.T) {
	for _, args := range [][]string{
		{"policy"}, {"policy", "list", "--json"}, {"rename", "dev", "prod"},
		{"extend", "dev", "--by", "1h"}, {"convert", "dev"}, {"resize", "dev", "--cpu", "4"}, {"wait", "dev", "cmd-1"},
	} {
		cmd := newCellaCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "'latere cella "+args[0]+"' is no longer available: ") {
			t.Errorf("%v: err = %v, want the reason it was removed", args, err)
		}
	}
	cmd := newCellaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"frobnicate"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `unknown command "frobnicate"`) {
		t.Errorf("unknown word: %v", err)
	}
	for _, c := range newCellaCmd().Commands() {
		if _, removed := removedCellaCommands[c.Name()]; removed {
			t.Errorf("%s is registered and listed as removed", c.Name())
		}
	}
}

// The flags of the retired API are gone with it.
func TestRemovedCellaFlags(t *testing.T) {
	group := newCellaCmd()
	for sub, flags := range map[string][]string{
		"apply": {"credential", "idempotency-key"},
		"run":   {"credential", "follow", "detach", "idempotency-key"},
		"shell": {"session"},
	} {
		cmd, _, err := group.Find([]string{sub})
		if err != nil || cmd.Name() != sub {
			t.Fatalf("find %s: %v", sub, err)
		}
		for _, name := range flags {
			if cmd.Flags().Lookup(name) != nil {
				t.Errorf("%s still has --%s", sub, name)
			}
		}
		if len(cmd.Commands()) != 0 {
			t.Errorf("%s still has subcommands", sub)
		}
	}
}

// countingTransport counts the requests that pass through it.
type countingTransport struct {
	next http.RoundTripper
	n    atomic.Int32
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.next.RoundTrip(r)
}

// Every request, the attach socket included, goes through the CLI's own HTTP
// client, whose transport wraps http.DefaultTransport, and never through the
// exported client's default transport.
func TestCellaRequestsUseTheCLIClient(t *testing.T) {
	f := newFakeCore(t)
	f.attach = func(conn *websocket.Conn) {
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Errorf("first frame: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"exit":0}`))
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}
	counting := &countingTransport{next: http.DefaultTransport}
	original := http.DefaultTransport
	http.DefaultTransport = counting
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, args := range [][]string{{"list"}, {"get", "dev"}, {"shell", "dev"}, {"cat", "dev", "a"}} {
		if _, _, err := f.runCella("", args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if got, want := int(counting.n.Load()), len(f.all()); got != want || want != 4 {
		t.Errorf("the CLI's transport carried %d requests, the core received %d, want 4 each", got, want)
	}
}

// The bearer is minted for the audience cella from the saved login, reused
// while it is valid, and minted again once it is within the margin of its
// expiry.
func TestCellaTokenMintedAndReminted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expiresIn int64
		mints     int32
	}{
		{"reused", 3600, 1},
		{"reminted", 30, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTokens(t)
			writeAuthTokenFile(t, "login-access", "login-refresh", time.Now().Add(time.Hour))
			var mints atomic.Int32
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/actor-tokens" {
					http.NotFound(w, r)
					return
				}
				var body struct {
					Audience string `json:"audience"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Audience != "cella" {
					t.Errorf("minted for %q, want cella", body.Audience)
				}
				n := mints.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"actor_token": fakeJWT(t, map[string]any{"aud": body.Audience, "n": n}),
					"expires_in":  tc.expiresIn,
				})
			}))
			t.Cleanup(auth.Close)
			source := mintedCellaToken(auth.URL)
			first, err := source.Token(t.Context())
			if err != nil {
				t.Fatalf("first token: %v", err)
			}
			second, err := source.Token(t.Context())
			if err != nil {
				t.Fatalf("second token: %v", err)
			}
			if got := mints.Load(); got != tc.mints {
				t.Errorf("mints = %d, want %d", got, tc.mints)
			}
			if (first == second) != (tc.mints == 1) {
				t.Errorf("first and second tokens equal = %v with %d mints", first == second, tc.mints)
			}
		})
	}
}

// A saved login that cannot mint fails the command before anything reaches
// the control plane.
func TestCellaTokenWithoutLogin(t *testing.T) {
	f := newFakeCore(t)
	isolateTokens(t)
	t.Setenv("AUTH_URL", f.srv.URL)
	if _, _, err := f.runCella("", "list"); err == nil || !strings.Contains(err.Error(), "cannot authenticate to Cella") {
		t.Fatalf("err = %v", err)
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}
