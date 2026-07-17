package api

import (
	"strings"
	"testing"
)

// TestInferAuthURL pins whoami's auth-host derivation. The old whoami used
// strings.Replace(BaseURL, "cella.", "auth.", 1), which silently no-ops when
// the host has no literal "cella." label, leaving /tokeninfo pointed at the
// wrong same host. InferAuthURL swaps the leading label robustly.
func TestInferAuthURL(t *testing.T) {
	cases := map[string]string{
		"":                          "https://auth.latere.ai",
		"https://cella.latere.ai":   "https://auth.latere.ai",
		"https://api.example.com":   "https://auth.example.com",
		"http://cella.localhost:80": "http://auth.localhost:80",
		"://not-a-url":              "https://auth.latere.ai",
		"https://localhost":         "https://auth.latere.ai",
	}
	for in, want := range cases {
		if got := InferAuthURL(in); got != want {
			t.Errorf("InferAuthURL(%q) = %q, want %q", in, got, want)
		}
	}

	// The brittle strings.Replace would have left a non-cella host unchanged
	// (the bug); InferAuthURL must not.
	const nonCella = "https://api.example.com"
	if old := strings.Replace(nonCella, "cella.", "auth.", 1); InferAuthURL(nonCella) == old {
		t.Errorf("InferAuthURL(%q) still resolves to the same (wrong) host %q", nonCella, old)
	}
}

