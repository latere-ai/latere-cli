// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

// TestCeApplyFlag locks the contract that `latere cella apply` only
// accepts -f. The old flag-soup `cella create --image --tier ...`
// surface was retired in favour of one declarative path so users
// can author Manifests once and reuse them across every surface
// (dashboard YAML tab, public API, CLI).
func TestCeApplyFlag(t *testing.T) {
	cmd := newCeApplyCmd()
	if cmd.Use != "apply" {
		t.Fatalf("apply Use = %q, want %q", cmd.Use, "apply")
	}
	f := cmd.Flags().Lookup("file")
	if f == nil {
		t.Fatal("apply: -f/--file flag missing")
	}
	if f.Shorthand != "f" {
		t.Errorf("--file shorthand = %q, want %q", f.Shorthand, "f")
	}
	// Old flag-based knobs must not come back unannounced.
	for _, gone := range []string{"image", "tier", "disk", "cpu", "memory", "env", "credential", "policy", "ttl"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("apply: legacy --%s flag survived the retire — Manifest should carry every knob", gone)
		}
	}
}

// TestCeApplyPostsRawYAML proves the body the server sees is the
// raw file bytes with Content-Type: application/yaml. This is the
// soft-shim contract the Cella API accepts — same body as the
// dashboard's YAML tab and a curl with --data-binary.
func TestCeApplyPostsRawYAML(t *testing.T) {
	var gotBody []byte
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sb-test","name":"dev"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "sandbox.yaml")
	const manifest = `apiVersion: cella.latere.ai/v1
kind: Sandbox
metadata:
  name: dev
spec:
  image: ghcr.io/latere-ai/sandbox-base:latest
  tier: ephemeral
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &api.Client{BaseURL: srv.URL, Token: "test", HTTP: srv.Client()}
	body, err := readManifestBody(manifestPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sb sandboxDTO
	if err := c.Do(t.Context(), http.MethodPost, "/v1/sandboxes",
		bytes.NewReader(body), "application/yaml", &sb); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if string(gotBody) != manifest {
		t.Errorf("server saw body %q, want raw manifest %q", gotBody, manifest)
	}
	if !strings.HasPrefix(gotCT, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml prefix", gotCT)
	}
}

func TestReadManifestBody_Stdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	const text = "apiVersion: cella.latere.ai/v1\nkind: Sandbox\nspec:\n  image: x\n"
	go func() {
		_, _ = w.WriteString(text)
		_ = w.Close()
	}()
	defer r.Close()
	got, err := readManifestBody("-", r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != text {
		t.Errorf("got %q, want %q", got, text)
	}
}

func TestReadManifestBody_Empty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(p, []byte("   \n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifestBody(p, nil); err == nil {
		t.Fatal("expected error for whitespace-only manifest")
	}
}

func TestReadManifestBody_OverLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.yaml")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 64<<10+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readManifestBody(p, nil)
	if err == nil {
		t.Fatal("expected error for manifest over 64 KiB limit")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error %q does not mention the limit", err.Error())
	}
}
