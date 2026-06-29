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
			// Long description text appears in topos --help output.
			want: []string{
				"Manage agents on the Topos control plane",
				"topos.latere.ai",
				"TOPOS_API_URL",
				"latere auth login",
				"TOPOS_DEV_AUTH",
				"TOPOS_DEV_TOKEN",
			},
		},
		{
			name: "topos agents list",
			args: []string{"topos", "agents", "list", "--help"},
			// Long description text + flag descriptions appear in list --help output.
			want: []string{
				"List agents registered on the Topos control plane.",
				"TOPOS_API_URL",
				"TOPOS_TOKEN",
				"override Topos API base URL",
			},
		},
		{
			name: "topos agents get",
			args: []string{"topos", "agents", "get", "--help"},
			want: []string{
				"Fetch one agent by id",
				"override Topos API base URL",
			},
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
		})
	}
}

// ---- client request / response tests ----

// writeTokenFile writes a minimal token.json to dir and returns the path. It
// sets LATERE_TOKEN_FILE (the Cella token NewClient reads) and ALSO writes an
// auth-token.json under LATERE_AUTH_TOKEN_FILE carrying the same bearer, because
// the Topos path (toposClient) authenticates with the auth root token, not the
// Cella token. Isolating both keeps tests off ~/.config/latere — without the
// auth-token isolation a developer's real login would leak into the assertions.
func writeTokenFile(t *testing.T, dir, token string) string {
	t.Helper()
	p := filepath.Join(dir, "token.json")
	data := `{"access_token":"` + token + `","token_type":"Bearer"}`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("writeTokenFile: %v", err)
	}
	ap := filepath.Join(dir, "auth-token.json")
	if err := os.WriteFile(ap, []byte(data), 0o600); err != nil {
		t.Fatalf("writeTokenFile (auth): %v", err)
	}
	t.Setenv("LATERE_AUTH_TOKEN_FILE", ap)
	return p
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
	tokenPath := writeTokenFile(t, dir, bearerToken)
	t.Setenv("LATERE_TOKEN_FILE", tokenPath)

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
	wantAuth := "Bearer " + bearerToken
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
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
	tokenPath := writeTokenFile(t, dir, "tok")
	t.Setenv("LATERE_TOKEN_FILE", tokenPath)

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
	tokenPath := writeTokenFile(t, dir, "tok")
	t.Setenv("LATERE_TOKEN_FILE", tokenPath)

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
	tokenPath := writeTokenFile(t, dir, "tok")
	t.Setenv("LATERE_TOKEN_FILE", tokenPath)

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
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(t.TempDir(), "nonexistent.json"))
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
	if !strings.Contains(err.Error(), "latere auth login") {
		t.Errorf("error doesn't mention auth login: %v", err)
	}
}

// TestToposClientUsesAuthRootToken pins the Option-B behaviour: the Topos path
// authenticates with the auth root token (aud=topos, run:agents), not the
// Cella-audience token that `latere cella` uses. token.json and auth-token.json
// carry different bearers; toposClient must pick the auth one.
func TestToposClientUsesAuthRootToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(dir, "token.json"))
	if err := os.WriteFile(filepath.Join(dir, "token.json"),
		[]byte(`{"access_token":"cella-token","token_type":"Bearer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAuthTokenFile(t, "auth-root-token", "", time.Time{})
	t.Setenv("TOPOS_TOKEN", "")

	c, err := toposClient("http://localhost:8080")
	if err != nil {
		t.Fatalf("toposClient: %v", err)
	}
	if c.Token != "auth-root-token" {
		t.Fatalf("Token = %q, want the auth root token (not the cella token)", c.Token)
	}
}

// TestToposTokenEnvOverride verifies TOPOS_TOKEN supplies the bearer
// directly for local development, bypassing the token file and login.
func TestToposTokenEnvOverride(t *testing.T) {
	// Ensure no token file is present for either subtest.
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(t.TempDir(), "nonexistent.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "nonexistent.json"))

	t.Run("TOPOS_TOKEN satisfies auth without a token file", func(t *testing.T) {
		t.Setenv("TOPOS_TOKEN", "dev-secret")
		c, err := toposClient("http://localhost:8080")
		if err != nil {
			t.Fatalf("toposClient: %v", err)
		}
		if c.Token != "dev-secret" {
			t.Errorf("Token = %q, want dev-secret", c.Token)
		}
	})

	t.Run("absent TOPOS_TOKEN still requires login", func(t *testing.T) {
		t.Setenv("TOPOS_TOKEN", "")
		if _, err := toposClient("http://localhost:8080"); err == nil {
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
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), bearerToken))

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
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "tok"))
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
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), bearerToken))

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
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "tok"))
	root := NewRoot("test")
	root.SetErr(&strings.Builder{})
	root.SetOut(&strings.Builder{})
	root.SetArgs([]string{"topos", "session", "create", "agent_01hxy"})
	if err := root.Execute(); err == nil {
		t.Fatal("session create without --prompt = nil error, want a required error")
	}
}
