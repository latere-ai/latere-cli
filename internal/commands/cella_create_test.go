package commands

import "testing"

// TestCeCreateDefaultImageInCatalog locks the `cella create --image`
// default to the only sandbox-base ref the server's image catalog
// accepts. Sandboxd publishes the curated list at
// sandbox/internal/imagecatalog (sandbox-base, sandbox-claude,
// sandbox-codex, sandbox-agents, all `:latest`). A previous default of
// `:main` was rejected with `image_not_allowed`, breaking the
// out-of-the-box `latere cella create` invocation.
func TestCeCreateDefaultImageInCatalog(t *testing.T) {
	cmd := newCeCreateCmd()
	flag := cmd.Flags().Lookup("image")
	if flag == nil {
		t.Fatal("cella create: --image flag missing")
	}
	const want = "ghcr.io/latere-ai/sandbox-base:latest"
	if got := flag.DefValue; got != want {
		t.Fatalf("--image default = %q, want %q (must be a server-catalog ref)", got, want)
	}
}
