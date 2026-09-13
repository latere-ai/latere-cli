// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- wiring tests ----

// TestToposCommandRegisteredInRoot verifies that 'latere topos' and its
// 'agents list' subcommand are reachable through the root command tree.
func TestToposCommandRegisteredInRoot(t *testing.T) {
	root := NewRoot("test")

	// topos must appear in root's commands.
	var found bool
	for _, cmd := range root.Commands() {
		if cmd.Name() == "topos" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("'topos' command not registered in root")
	}
}

func TestToposHelpText(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "topos",
			args: []string{"topos", "--help"},
			want: []string{"agent platform", "--local"},
		},
		{
			name: "topos agents list",
			args: []string{"topos", "agents", "list", "--help"},
			want: []string{"List the agents you can run"},
		},
		{
			name: "topos agents get",
			args: []string{"topos", "agents", "get", "--help"},
			want: []string{"Fetch one agent by id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := executeForHelp(NewRoot("test"), tc.args...)
			if err != nil {
				t.Fatalf("help command failed: %v\noutput:\n%s", err, got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("help output missing %q\noutput:\n%s", want, got)
				}
			}
			// User-facing help must not leak developer/internal details.
			for _, banned := range []string{"TOPOS_API_URL", "TOPOS_TOKEN", "TOPOS_DEV_AUTH", "TOPOS_DEV_TOKEN", "localhost:8080"} {
				if strings.Contains(got, banned) {
					t.Errorf("help output leaks dev detail %q\noutput:\n%s", banned, got)
				}
			}
		})
	}
}

// TestToposPrintWithoutLocalRejected pins that a prompt given to the root
// command without --local is refused. Before this, `latere topos -p "..."`
// parsed cleanly and opened the hosted home with the prompt discarded, so a
// user copying that form from the docs got no answer and no error.
func TestToposPrintWithoutLocalRejected(t *testing.T) {
	root := NewRoot("test")
	var errOut bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errOut)
	root.SetArgs([]string{"topos", "-p", "explain this repo"})
	err := root.Execute()
	if err == nil {
		t.Fatal("topos -p without --local ran without error; the prompt was silently dropped")
	}
	for _, want := range []string{"--local", "session start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not point the user at %q", err, want)
		}
	}
}

// ---- client request / response tests ----

// writeTokenFile writes a login to dir, points the CLI at it, and points
// AUTH_URL at a mint stub the test owns. Every product path mints from the
// login, so this is all a command test needs to stay off ~/.config/latere.
func writeTokenFile(t *testing.T, dir, token string) string {
	t.Helper()
	p := filepath.Join(dir, "auth-token.json")
	data := `{"access_token":"` + token + `","token_type":"Bearer"}`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("writeTokenFile: %v", err)
	}
	t.Setenv("LATERE_AUTH_TOKEN_FILE", p)
	t.Setenv("AUTH_URL", authMintStub(t))
	return p
}

// toposActorToken is the bearer a product receives in these tests: the actor
// token authMintStub hands back, never the root token on disk.
const toposActorToken = "topos-actor-token"

// authMintStub stands in for auth so a command that mints a product
// credential reaches a server the test owns instead of auth.latere.ai. It
// answers the mint and the refresh; any other path is a 404, which surfaces
// as the command's own auth error.
func authMintStub(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/actor-tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"actor_token": toposActorToken, "expires_in": 300})
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "refreshed-root", "token_type": "Bearer", "expires_in": 3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestToposAgentsListCallsCorrectEndpoint verifies that 'latere topos
// agents list' sends GET /v1/agents with the correct Authorization header
// and decodes the response envelope.
func TestToposAgentsListCallsCorrectEndpoint(t *testing.T) {
	const bearerToken = "test-bearer-token"

	// Fake Topos API server.
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []map[string]any{
				{
					"id":           "agent_01hxy",
					"display_name": "My Agent",
					"kind":         "assistant",
					"org_id":       "org_abc",
					"owner_sub":    "sub_xyz",
				},
			},
		})
	}))
	defer srv.Close()

	// Point CLI at the fake server via TOPOS_API_URL.
	t.Setenv("TOPOS_API_URL", srv.URL)

	// Write a token file so MustRequireAuth passes.
	dir := t.TempDir()
	writeTokenFile(t, dir, bearerToken)

	// captureStdout (defined in auth_test.go) captures os.Stdout.
	output, execErr := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "agents", "list"})
		return root.Execute()
	})

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}

	// Validate request shape.
	if gotPath != "/v1/agents" {
		t.Errorf("request path = %q, want /v1/agents", gotPath)
	}
	// Topos receives the minted actor token, never the root token on disk.
	wantAuth := "Bearer " + toposActorToken
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	if strings.Contains(gotAuth, bearerToken) {
		t.Errorf("Authorization = %q leaks the root token to Topos", gotAuth)
	}

	// Validate output contains agent fields.
	for _, want := range []string{"agent_01hxy", "My Agent", "assistant", "org_abc"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

// TestToposAgentsListEmptyResponse verifies the empty-list message is
// printed when the server returns no agents.
func TestToposAgentsListEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"agents": []any{}})
	}))
	defer srv.Close()

	t.Setenv("TOPOS_API_URL", srv.URL)
	dir := t.TempDir()
	writeTokenFile(t, dir, "tok")

	output, execErr := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "agents", "list"})
		return root.Execute()
	})

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if !strings.Contains(output, "No agents") {
		t.Errorf("expected empty-list message, got: %q", output)
	}
}

// TestToposAgentsListJSONOutput verifies --json emits valid JSON.
func TestToposAgentsListJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []map[string]any{
				{"id": "agent_42", "kind": "worker", "display_name": "W"},
			},
		})
	}))
	defer srv.Close()

	t.Setenv("TOPOS_API_URL", srv.URL)
	dir := t.TempDir()
	writeTokenFile(t, dir, "tok")

	output, execErr := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "agents", "list", "--json"})
		return root.Execute()
	})

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}

	var agents []agentDTO
	if err := json.Unmarshal([]byte(output), &agents); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, output)
	}
	if len(agents) != 1 || agents[0].ID != "agent_42" {
		t.Errorf("unexpected agents: %+v", agents)
	}
}

// TestToposAgentsGetCallsCorrectEndpoint verifies `latere topos agents
// get <id>` hits /v1/agents/<id> with the Bearer token.
func TestToposAgentsGetCallsCorrectEndpoint(t *testing.T) {
	const agentID = "agent_test123"
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentDTO{
			ID:          agentID,
			DisplayName: "Test Agent",
			Kind:        "router",
		})
	}))
	defer srv.Close()

	t.Setenv("TOPOS_API_URL", srv.URL)
	dir := t.TempDir()
	writeTokenFile(t, dir, "tok")

	output, execErr := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "agents", "get", agentID})
		return root.Execute()
	})

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}

	wantPath := "/v1/agents/" + agentID
	if gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(output, agentID) {
		t.Errorf("output missing agent id %q:\n%s", agentID, output)
	}
}

// TestToposURLResolution verifies that resolveToposURL priority order
// is: flag > TOPOS_API_URL env > default.
func TestToposURLResolution(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv("TOPOS_API_URL", "http://env-url")
		got := resolveToposURL("http://flag-url")
		if got != "http://flag-url" {
			t.Errorf("got %q, want flag value", got)
		}
	})

	t.Run("env wins over default", func(t *testing.T) {
		t.Setenv("TOPOS_API_URL", "http://env-url")
		got := resolveToposURL("")
		if got != "http://env-url" {
			t.Errorf("got %q, want env value", got)
		}
	})

	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv("TOPOS_API_URL", "")
		got := resolveToposURL("")
		if got != "https://topos.latere.ai" {
			t.Errorf("got %q, want default", got)
		}
	})
}

// TestToposRequiresAuth verifies that 'agents list' fails with the
// not-logged-in error when no token file exists.
func TestToposRequiresAuth(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "nonexistent.json"))
	t.Setenv("TOPOS_API_URL", "http://localhost:1") // unreachable; error is pre-flight

	root := NewRoot("test")
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"topos", "agents", "list"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}
	if !strings.Contains(err.Error(), "latere login") {
		t.Errorf("error doesn't mention auth login: %v", err)
	}
}

// The Topos path mints a token for toposAudience from the saved login.
// The login token, which names the auth issuer, is never presented to
// Topos.
func TestToposClientMintsToposActorToken(t *testing.T) {
	writeAuthTokenFile(t, "auth-root-token", "", time.Time{})
	t.Setenv("TOPOS_TOKEN", "")

	var gotBearer, gotAudience string
	var gotTTL float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/actor-tokens" {
			t.Errorf("unexpected auth request %s", r.URL.Path)
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotBearer = r.Header.Get("Authorization")
		gotAudience, _ = body["audience"].(string)
		gotTTL, _ = body["ttl_seconds"].(float64)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"actor_token": toposActorToken, "expires_in": 300})
	}))
	defer srv.Close()
	t.Setenv("AUTH_URL", srv.URL)

	c, err := toposClient(t.Context(), "http://localhost:8080")
	if err != nil {
		t.Fatalf("toposClient: %v", err)
	}
	if c.Token != toposActorToken {
		t.Fatalf("Token = %q, want the minted actor token", c.Token)
	}
	if gotBearer != "Bearer auth-root-token" {
		t.Errorf("mint bearer = %q, want the auth root token", gotBearer)
	}
	if gotAudience != toposAudience || gotTTL != 300 {
		t.Errorf("mint = audience %q ttl %v, want %q and 300", gotAudience, gotTTL, toposAudience)
	}
}

// TestToposTokenEnvOverride verifies TOPOS_TOKEN supplies the bearer
// directly for local development, bypassing the token file and login.
func TestToposTokenEnvOverride(t *testing.T) {
	// Ensure no token file is present for either subtest.
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "nonexistent.json"))

	t.Run("TOPOS_TOKEN satisfies auth without a token file", func(t *testing.T) {
		t.Setenv("TOPOS_TOKEN", "dev-secret")
		c, err := toposClient(t.Context(), "http://localhost:8080")
		if err != nil {
			t.Fatalf("toposClient: %v", err)
		}
		if c.Token != "dev-secret" {
			t.Errorf("Token = %q, want dev-secret", c.Token)
		}
	})

	t.Run("absent TOPOS_TOKEN still requires login", func(t *testing.T) {
		t.Setenv("TOPOS_TOKEN", "")
		if _, err := toposClient(t.Context(), "http://localhost:8080"); err == nil {
			t.Error("expected not-logged-in error without a token file or TOPOS_TOKEN")
		}
	})
}

// TestToposAgentsCreatePostsBody verifies 'latere topos agents create'
// POSTs the agent body to /v1/agents and prints the created agent.
func TestToposAgentsCreatePostsBody(t *testing.T) {
	const bearerToken = "create-token"

	var gotMethod, gotPath string
	var gotBody createAgentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "agent_new1", "display_name": gotBody.DisplayName, "kind": gotBody.Kind,
		})
	}))
	defer srv.Close()

	t.Setenv("TOPOS_API_URL", srv.URL)
	writeTokenFile(t, t.TempDir(), bearerToken)

	output, execErr := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "agents", "create", "--name", "Build Bot", "--kind", "worker"})
		return root.Execute()
	})
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/agents" {
		t.Fatalf("request = %s %s, want POST /v1/agents", gotMethod, gotPath)
	}
	if gotBody.DisplayName != "Build Bot" || gotBody.Kind != "worker" {
		t.Fatalf("posted body = %+v", gotBody)
	}
	if !strings.Contains(output, "agent_new1") {
		t.Fatalf("output missing created id:\n%s", output)
	}
}

// TestToposAgentsCreateRequiresNameAndKind verifies the client-side guard.
func TestToposAgentsCreateRequiresNameAndKind(t *testing.T) {
	t.Setenv("LATERE_CELLA_TOKEN", "tok")
	root := NewRoot("test")
	root.SetErr(&strings.Builder{})
	root.SetOut(&strings.Builder{})
	root.SetArgs([]string{"topos", "agents", "create", "--name", "OnlyName"})
	if err := root.Execute(); err == nil {
		t.Fatal("create without --kind = nil error, want a required-flag error")
	}
}

// TestToposSessionCreatePostsPrompt verifies 'latere topos session create
// <id>' POSTs the prompt to the agent's session endpoint and prints the
// run result.
func TestToposSessionCreatePostsPrompt(t *testing.T) {
	const bearerToken = "run-token"

	var gotPath string
	var gotBody sessionCreateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "sess_abc", "sandbox_id": "sb_1",
			"output": "Task completed", "stop_reason": "end_turn", "tool_calls": 2,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	t.Setenv("TOPOS_API_URL", srv.URL)
	writeTokenFile(t, t.TempDir(), bearerToken)

	output, execErr := captureStdout(func() error {
		root := NewRoot("test")
		root.SetErr(&strings.Builder{})
		root.SetArgs([]string{"topos", "session", "create", "agent_01hxy", "--prompt", "list files"})
		return root.Execute()
	})
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if gotPath != "/v1/agents/agent_01hxy/sessions" {
		t.Fatalf("request path = %q, want /v1/agents/agent_01hxy/sessions", gotPath)
	}
	if gotBody.Prompt != "list files" {
		t.Fatalf("posted prompt = %q", gotBody.Prompt)
	}
	for _, want := range []string{"sess_abc", "end_turn", "Task completed"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

// TestToposSessionCreateRequiresPrompt verifies the client-side guard.
func TestToposSessionCreateRequiresPrompt(t *testing.T) {
	t.Setenv("LATERE_CELLA_TOKEN", "tok")
	root := NewRoot("test")
	root.SetErr(&strings.Builder{})
	root.SetOut(&strings.Builder{})
	root.SetArgs([]string{"topos", "session", "create", "agent_01hxy"})
	if err := root.Execute(); err == nil {
		t.Fatal("session create without --prompt = nil error, want a required error")
	}
}
