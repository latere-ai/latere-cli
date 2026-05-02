package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

// Cross-repo regression coverage for sandbox spec 66 against the CLI
// surface. Three things matter:
//
//   1. Every sandboxd write the CLI / MCP makes that can carry a
//      credential reference uses `credential_catalog`, not a literal
//      `secret_env` map.
//   2. Empty selection omits the field so the server sees the legacy
//      "attach full client catalog" default rather than an explicit
//      empty-attach.
//   3. The MCP tool argument schemas advertise the field as "trust-plane
//      catalog keys; not secret values" so an agent host cannot
//      misinterpret the field as a place for plaintext credentials.

func TestSpec66_CLIBuildersUseCredentialCatalogNotSecretEnv(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{
			"oneShotRunBody full set",
			oneShotRunBody(
				[]string{"echo", "hi"},
				map[string]string{"FOO": "bar"},
				"/workspace", "img:v1", 5, "1", "1Gi", 60,
				[]string{"llm-primary"},
			),
		},
		{
			"oneShotRunBody empty selection",
			oneShotRunBody(
				[]string{"echo", "hi"},
				nil, "", "", 0, "", "", 0,
				nil,
			),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.body["secret_env"]; ok {
				t.Fatal("body emits secret_env; spec 50 removed it and spec 66 forbids reintroduction")
			}
			if reflect.DeepEqual(tc.body["credential_catalog"], []string{}) {
				t.Fatal("empty selection should omit credential_catalog (sends nil), not encode []")
			}
		})
	}

	full := cases[0].body
	got, _ := full["credential_catalog"].([]string)
	if len(got) != 1 || got[0] != "llm-primary" {
		t.Fatalf("credential_catalog = %v, want [llm-primary]", got)
	}

	empty := cases[1].body
	if _, ok := empty["credential_catalog"]; ok {
		t.Fatalf("empty selection should omit field, got %v", empty["credential_catalog"])
	}
}

func TestSpec66_StartCommandSendsCredentialCatalog(t *testing.T) {
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/commands") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &lastBody)
		_, _ = w.Write([]byte(`{"command_id":"c-1","phase":"running"}`))
	}))
	defer srv.Close()
	c := api.NewClient(srv.URL)

	if _, err := startCommand(context.Background(), c, "sb-x",
		[]string{"echo", "hi"}, nil, "", []string{"llm-primary", "git-credentials"}); err != nil {
		t.Fatalf("startCommand: %v", err)
	}
	got, _ := lastBody["credential_catalog"].([]any)
	if len(got) != 2 || got[0] != "llm-primary" || got[1] != "git-credentials" {
		t.Fatalf("server-side credential_catalog=%v, want [llm-primary git-credentials]", got)
	}
	if _, ok := lastBody["secret_env"]; ok {
		t.Fatal("startCommand emitted secret_env")
	}

	// No selection: field must be omitted entirely so the server uses
	// the spec-50 default of attaching the full client catalog.
	lastBody = nil
	if _, err := startCommand(context.Background(), c, "sb-x",
		[]string{"echo", "hi"}, nil, "", nil); err != nil {
		t.Fatalf("startCommand without catalog: %v", err)
	}
	if _, ok := lastBody["credential_catalog"]; ok {
		t.Fatalf("empty selection emitted credential_catalog: %v", lastBody["credential_catalog"])
	}
}

func TestSpec66_MCPCreateAndRunSendCredentialCatalog(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		switch {
		case r.URL.Path == "/v1/sandboxes":
			_, _ = w.Write([]byte(`{"id":"sb-1","name":"demo","state":"running"}`))
		case strings.HasSuffix(r.URL.Path, "/commands"):
			_, _ = w.Write([]byte(`{"command_id":"c-1","phase":"running"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tools := &mcpTools{c: api.NewClient(srv.URL)}

	_, _, err := tools.create(context.Background(), nil, mcpCreateArgs{
		Image: "img", Tier: "ephemeral", DiskGB: 5,
		CredentialCatalog: []string{"llm-primary"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err = tools.run(context.Background(), nil, mcpRunArgs{
		Sandbox: "sb-1", Argv: []string{"echo", "hi"},
		CredentialCatalog: []string{"git-credentials"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 server hits, got %d (%v)", len(bodies), paths)
	}
	for i, body := range bodies {
		if _, ok := body["secret_env"]; ok {
			t.Errorf("MCP request %d emitted secret_env: %s", i, paths[i])
		}
		got, _ := body["credential_catalog"].([]any)
		if len(got) == 0 {
			t.Errorf("MCP request %d missing credential_catalog: %s body=%+v", i, paths[i], body)
		}
	}
}

func TestSpec66_MCPSchemasDescribeCatalogAsNonSecret(t *testing.T) {
	for _, want := range []string{
		`mcp:"trust-plane catalog keys to attach; not secret values"`,
		`mcp:"trust-plane catalog keys to use for this command; not secret values"`,
	} {
		if !mcpSourceContains(t, want) {
			t.Errorf("mcp.go schema text missing %q", want)
		}
	}
}

// mcpSourceContains is a tiny grep against the source so the schema
// description text — which agents read at tool-discovery time — is
// pinned to a wording that explicitly rejects "secret value" usage.
// Reading the file is brittle but cheaper than parsing struct tags via
// reflection because the `mcp:` tag is not a stdlib struct tag.
func mcpSourceContains(t *testing.T, needle string) bool {
	t.Helper()
	const path = "mcp.go"
	b, err := readSourceFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Contains(string(b), needle)
}

func readSourceFile(name string) ([]byte, error) {
	// Tests run from the package dir, so a relative read suffices.
	return os.ReadFile(name)
}
