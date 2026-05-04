package commands

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyListPrintsCreateGuidanceFields(t *testing.T) {
	writeTestToken(t)
	var authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/policies" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"name":"agent-default",
				"label":"Agent Default",
				"description":"Uses sidecar credentials",
				"capability_profile":"restricted",
				"sidecar_required":true,
				"is_default":true,
				"selectable":true,
				"assignment_source":"default"
			},
			{
				"name":"restricted-network",
				"label":"Restricted Network",
				"description":"No sidecar required",
				"capability_profile":"restricted-no-network",
				"sidecar_required":false,
				"is_default":false,
				"selectable":true,
				"assignment_source":"client"
			}
		]`))
	}))
	defer srv.Close()

	out := capturePolicyStdout(t, func() {
		if err := runPolicyList(context.Background(), srv.URL, false); err != nil {
			t.Fatalf("runPolicyList: %v", err)
		}
	})

	if authz != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want bearer token", authz)
	}
	for _, want := range []string{
		"policy:     agent-default",
		"default:    yes",
		"selectable: yes",
		"sidecar:    yes",
		"agent-default",
		"restricted-network",
		"restricted-no-network",
		"source:     client",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("policy list output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "NAME") || strings.Contains(out, "\t") {
		t.Fatalf("policy list should be pure text, not a wide table:\n%s", out)
	}
}

func TestPolicyListEmptyExplainsNextStep(t *testing.T) {
	writeTestToken(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	out := capturePolicyStdout(t, func() {
		if err := runPolicyList(context.Background(), srv.URL, false); err != nil {
			t.Fatalf("runPolicyList: %v", err)
		}
	})

	for _, want := range []string{
		"No policy profiles are visible",
		"latere cella create --policy <name>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("empty policy output missing %q:\n%s", want, out)
		}
	}
}

func TestSandboxListPrintsReadableRecords(t *testing.T) {
	out := capturePolicyStdout(t, func() {
		printSandboxList([]sandboxDTO{
			{
				ID:       "sb-019dc976-2b28-7c55-8778-bf7d5ae6c58d",
				Name:     "workspace-1",
				State:    "stopped",
				Tier:     "persistent",
				DiskGB:   5,
				CPUMilli: 1000,
				MemoryMB: 2048,
			},
			{
				ID:     "sb-019dc976-2b28-7c55-8778-warm-pool",
				Name:   "warm-pool",
				State:  "running",
				Tier:   "ephemeral",
				DiskGB: 5,
			},
		})
	})

	for _, want := range []string{
		"cella:      workspace-1",
		"id:         sb-019dc976-2b28-7c55-8778-bf7d5ae6c58d",
		"state:      stopped",
		"tier:       persistent",
		"resources:  cpu=1000m memory=2048Mi",
		"\n\ncella:      warm-pool",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sandbox list output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "NAME") || strings.Contains(out, "\t") {
		t.Fatalf("sandbox list should be pure text, not a wide table:\n%s", out)
	}
}

func writeTestToken(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"test-token","token_type":"Bearer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_TOKEN_FILE", path)
}

func capturePolicyStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
