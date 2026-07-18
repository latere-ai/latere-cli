package main

// Family production E2E: drives the whole Latere product family through
// one `latere` CLI identity and asserts each identity-fabric edge end to
// end against live production. It is the reproducible companion to
// specs/products/identity-fabric/release-and-verification.md: one login,
// then every edge a CLI user can reach (cella, lux, drive, topos, auth),
// plus the two invariants (owner-rooted subject, trust-root rule).
//
// Opt-in and tiered, because the higher tiers spend real money and mutate
// real state:
//
//	LATERE_FAMILY_E2E=1        read-only edges: whoami, /tokeninfo,
//	                           cella list, lux models+access, drive ls,
//	                           topos reachability, garbage-token 401.
//	                           No cost, no resource creation.
//	LATERE_FAMILY_E2E_WRITE=1  also: lux invoke (a token), drive put/get/rm
//	                           round-trip, create a cella + reach lux from
//	                           inside it (if-14), cross-product 401. Spends
//	                           money; cleans up after itself.
//	LATERE_FAMILY_E2E_LOGOUT=1 also: logout then reuse the old bearer ->
//	                           401 (if-11). Destructive: ends the session.
//
// Identity comes from the logged-in CLI (~/.config/latere/token.json) or
// LATERE_E2E_TOKEN. Run:
//
//	LATERE_FAMILY_E2E=1 go test ./cmd/latere/ -run TestFamilyE2E -v
//
// Service URLs default to production and are overridable:
// CELLA_API_URL, AUTH_URL, LUX_API_URL, DRIVE_API_URL, TOPOS_API_URL.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type familyEnv struct {
	bin       string // path to a freshly built latere binary
	token     string // cella-issued bearer (token.json), valid at cella
	cellaURL  string
	authURL   string
	luxURL    string
	driveURL  string
	toposURL  string
	sub       string // owner subject, read from whoami
	httpc     *http.Client
}

func requireFamilyE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("LATERE_FAMILY_E2E") != "1" {
		t.Skip("set LATERE_FAMILY_E2E=1 to run the live family e2e (hits production with your identity)")
	}
}

func urlOr(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

// setupFamily builds the latere binary under test, resolves the caller's
// bearer, and reads the owner subject once.
func setupFamily(t *testing.T) *familyEnv {
	t.Helper()
	fe := &familyEnv{
		cellaURL: urlOr("CELLA_API_URL", "https://cella.latere.ai"),
		authURL:  urlOr("AUTH_URL", "https://auth.latere.ai"),
		luxURL:   urlOr("LUX_API_URL", "https://lux.latere.ai"),
		driveURL: urlOr("DRIVE_API_URL", "https://drive.latere.ai"),
		toposURL: urlOr("TOPOS_API_URL", "https://topos.latere.ai"),
		httpc:    &http.Client{Timeout: 30 * time.Second},
	}

	// Build the binary under test so the e2e exercises the release
	// candidate, not a stale installed copy. It reads the same token.json.
	fe.bin = filepath.Join(t.TempDir(), "latere")
	build := exec.Command("go", "build", "-o", fe.bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build latere: %v\n%s", err, out)
	}

	// Resolve the bearer for direct HTTP checks.
	if tok := os.Getenv("LATERE_E2E_TOKEN"); tok != "" {
		fe.token = tok
	} else {
		out, _, err := fe.run(t, 20*time.Second, "print-token")
		if err != nil || strings.TrimSpace(out) == "" {
			t.Skipf("no bearer: log in with `latere login` or set LATERE_E2E_TOKEN (%v)", err)
		}
		fe.token = strings.TrimSpace(out)
	}
	return fe
}

// run executes the latere binary with a timeout and returns stdout/stderr.
func (fe *familyEnv) run(t *testing.T, d time.Duration, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, fe.bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// get issues a direct GET with an explicit bearer and returns status+body.
func (fe *familyEnv) get(t *testing.T, url, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := fe.httpc.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, string(body)
}

// freshBearer mints a short-lived auth-issued identity token via `lux env`.
// Unlike the on-disk auth-token.json (which goes stale between runs), this is
// freshly minted, so a direct /tokeninfo check stays green. Returns "" if lux
// access is unavailable.
func (fe *familyEnv) freshBearer(t *testing.T) string {
	t.Helper()
	out, _, err := fe.run(t, 30*time.Second, "lux", "env", "anthropic")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if _, v, ok := strings.Cut(strings.TrimSpace(line), "ANTHROPIC_AUTH_TOKEN="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// firstModel reads the first enabled model + its provider from `lux models`,
// so a live invoke uses whatever the identity actually has bound.
func (fe *familyEnv) firstModel(t *testing.T) (model, provider string) {
	t.Helper()
	out, _, err := fe.run(t, 30*time.Second, "lux", "models")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "model:" && model == "" {
			model = f[1]
		}
		if len(f) >= 2 && f[0] == "provider:" && provider == "" {
			provider = f[1]
		}
		if model != "" && provider != "" {
			break
		}
	}
	return model, provider
}

func TestFamilyE2E(t *testing.T) {
	requireFamilyE2E(t)
	fe := setupFamily(t)

	// --- Tier 1: read-only, no cost -------------------------------------

	// Edge: browser/CLI -> auth. Owner-rooted subject (invariant 1): the
	// principal is the owning user and the token verifies at auth.
	t.Run("auth/whoami-owner-rooted", func(t *testing.T) {
		out, errOut, err := fe.run(t, 20*time.Second, "whoami")
		if err != nil {
			t.Fatalf("whoami: %v\n%s", err, errOut)
		}
		if !strings.Contains(out, "principal:") || !strings.Contains(out, "sub:") {
			t.Fatalf("whoami output missing principal/sub:\n%s", out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "sub:") {
				fe.sub = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "sub:"))
			}
		}
		if fe.sub == "" {
			t.Fatal("could not read owner sub from whoami")
		}
	})

	// A fresh auth-issued bearer verifies at auth /tokeninfo and resolves to
	// the same owner (invariant 1). token.json is cella-issued and would 401
	// here by design (asserted separately below), so this mints a fresh
	// identity token via lux env rather than reusing the stale disk token.
	t.Run("auth/tokeninfo-verifies", func(t *testing.T) {
		bearer := fe.freshBearer(t)
		if bearer == "" {
			t.Skip("could not mint a fresh auth bearer via lux env")
		}
		status, body := fe.get(t, fe.authURL+"/tokeninfo", bearer)
		if status != http.StatusOK {
			t.Fatalf("/tokeninfo = %d, want 200\n%s", status, body)
		}
		var info map[string]any
		if err := json.Unmarshal([]byte(body), &info); err != nil {
			t.Fatalf("tokeninfo not json: %v\n%s", err, body)
		}
		if fe.sub != "" {
			if got, _ := info["sub"].(string); got != "" && got != fe.sub {
				t.Errorf("tokeninfo sub = %q, want owner %q (invariant 1)", got, fe.sub)
			}
		}
	})

	// Edge: CLI -> cella (actor token + exchange chain). Authorized listing.
	t.Run("cli->cella-list", func(t *testing.T) {
		_, errOut, err := fe.run(t, 30*time.Second, "cella", "list")
		if err != nil {
			t.Fatalf("cella list (authorization failed?): %v\n%s", err, errOut)
		}
	})

	// Edge: CLI -> lux (per-call actor token, no key allocation).
	t.Run("cli->lux-models", func(t *testing.T) {
		out, errOut, err := fe.run(t, 30*time.Second, "lux", "models")
		if err != nil {
			t.Fatalf("lux models: %v\n%s", err, errOut)
		}
		if strings.TrimSpace(out) == "" {
			t.Error("lux models returned nothing; expected models visible to the identity")
		}
	})

	// if-14 context: the access profile is what gates in-sandbox model use.
	t.Run("cli->lux-access", func(t *testing.T) {
		if _, errOut, err := fe.run(t, 30*time.Second, "lux", "access"); err != nil {
			t.Fatalf("lux access: %v\n%s", err, errOut)
		}
	})

	// Edge: CLI -> drive (per-request auth).
	t.Run("cli->drive-ls", func(t *testing.T) {
		if _, errOut, err := fe.run(t, 30*time.Second, "drive", "ls"); err != nil {
			t.Fatalf("drive ls: %v\n%s", err, errOut)
		}
	})

	// Edge: CLI/topos control plane reachability (the site authorizes).
	t.Run("cli->topos-reachable", func(t *testing.T) {
		status, _ := fe.get(t, fe.toposURL+"/", fe.token)
		if status >= 500 {
			t.Fatalf("topos %s unreachable: %d", fe.toposURL, status)
		}
	})

	// Invariant 2 (trust-root): a garbage bearer is rejected everywhere,
	// proving verification is on (not fail-open).
	t.Run("invariant2/garbage-token-rejected", func(t *testing.T) {
		for _, u := range []string{fe.authURL + "/tokeninfo", fe.cellaURL + "/v1/sandboxes"} {
			status, _ := fe.get(t, u, "garbage.not.a.jwt")
			if status != http.StatusUnauthorized && status != http.StatusForbidden {
				t.Errorf("%s with garbage bearer = %d, want 401/403", u, status)
			}
		}
	})

	// Invariant 2 with a REAL cross-issuer token: the cella-issued token is
	// only valid at cella, so auth rejects it. This is the trust-root rule
	// in action, not a failure (the CLI relies on this 401 by design).
	t.Run("invariant2/cella-token-rejected-at-auth", func(t *testing.T) {
		status, _ := fe.get(t, fe.authURL+"/tokeninfo", fe.token)
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("cella token at auth /tokeninfo = %d, want 401/403 (trust-root rule)", status)
		}
	})

	// --- Tier 2: write / cost (opt-in) ----------------------------------
	if os.Getenv("LATERE_FAMILY_E2E_WRITE") == "1" {
		fe.runWriteTier(t)
	} else {
		t.Log("Tier 2 (write/cost) skipped; set LATERE_FAMILY_E2E_WRITE=1 to run it")
	}

	// --- Tier 3: destructive logout (opt-in) ----------------------------
	if os.Getenv("LATERE_FAMILY_E2E_LOGOUT") == "1" {
		fe.runLogoutTier(t)
	} else {
		t.Log("Tier 3 (logout revocation) skipped; set LATERE_FAMILY_E2E_LOGOUT=1 to run it")
	}
}

// runWriteTier exercises the cost/mutation edges: a live lux completion, a
// drive round-trip, and the if-14 in-sandbox->lux path via a throwaway
// cella. Each cleans up after itself.
func (fe *familyEnv) runWriteTier(t *testing.T) {
	// Edge: CLI -> lux invoke (a real one-shot completion) using whatever
	// model the identity has bound.
	t.Run("cli->lux-invoke", func(t *testing.T) {
		model, provider := fe.firstModel(t)
		if model == "" {
			t.Skip("no lux model bound to this identity; run `latere lux access set`")
		}
		out, errOut, err := fe.run(t, 60*time.Second, "lux", "invoke",
			"--provider", provider, "--model", model, "--max-tokens", "16",
			"reply with the single word: ok")
		if err != nil {
			t.Fatalf("lux invoke (%s/%s): %v\n%s", provider, model, err, errOut)
		}
		if strings.TrimSpace(out) == "" {
			t.Error("lux invoke returned empty completion")
		}
	})

	// Edge: CLI -> drive round-trip (put, ls sees it, get matches, rm).
	t.Run("cli->drive-roundtrip", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "e2e-probe.txt")
		want := fmt.Sprintf("family-e2e %d", time.Now().UnixNano())
		if err := os.WriteFile(src, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		dest := "files/family-e2e-probe.txt"
		if _, errOut, err := fe.run(t, 40*time.Second, "drive", "put", src, dest); err != nil {
			t.Fatalf("drive put: %v\n%s", err, errOut)
		}
		t.Cleanup(func() { _, _, _ = fe.run(t, 30*time.Second, "drive", "rm", "--permanent", dest) })
		got, errOut, err := fe.run(t, 40*time.Second, "drive", "get", dest, "-o", "-")
		if err != nil {
			t.Fatalf("drive get: %v\n%s", err, errOut)
		}
		if !strings.Contains(got, want) {
			t.Errorf("drive get mismatch: got %q, want to contain %q", got, want)
		}
	})

	// The if-14 in-sandbox->lux edge is the flagship of this release. Its
	// builder (inSandboxLux) creates a model_access cella and reaches lux from
	// inside it; it needs a configured model_access policy and the cella lux
	// wiring (CELLA_LUX_BASE_URL + CELLA_LUX_KEYS_CLIENT_SECRET) live in prod.
	t.Run("if-14/in-sandbox->lux", func(t *testing.T) {
		if os.Getenv("LATERE_FAMILY_E2E_SANDBOX") != "1" {
			t.Skip("set LATERE_FAMILY_E2E_SANDBOX=1 to create a cella and reach lux from inside it (costs a running sandbox)")
		}
		fe.inSandboxLux(t)
	})
}

// inSandboxLux creates a model-access cella, runs in-sandbox code that calls
// lux through the injected ANTHROPIC_BASE_URL, and asserts two things (if-14):
// the code sees only an opaque placeholder key (never a real lux_ key, which
// rides solely in the egress substitution map), and the call still returns a
// completion. Needs sandbox if-14 released and a model_access policy named by
// LATERE_FAMILY_E2E_POLICY.
func (fe *familyEnv) inSandboxLux(t *testing.T) {
	policy := os.Getenv("LATERE_FAMILY_E2E_POLICY")
	if policy == "" {
		t.Skip("set LATERE_FAMILY_E2E_POLICY to a model_access policy name (needs sandbox if-14 released + a model_access policy configured)")
	}
	// The in-sandbox request must name a model the owner's lux access profile
	// binds (the per-sandbox key routes through that profile). Default to
	// claude-sonnet-5; override to match the owner's fallback binding.
	model := os.Getenv("LATERE_FAMILY_E2E_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}
	// The image must be a curated-catalog ref; the catalog is version-pinned
	// to the images release, so this is overridable as the catalog advances.
	image := os.Getenv("LATERE_FAMILY_E2E_IMAGE")
	if image == "" {
		image = "ghcr.io/latere-ai/sandbox-base:v0.0.15"
	}

	dir := t.TempDir()
	name := fmt.Sprintf("fam-e2e-if14-%d", time.Now().UnixNano()%1000000)
	manifest := filepath.Join(dir, "sandbox.yaml")
	spec := fmt.Sprintf("apiVersion: cella.latere.ai/v1\nkind: Sandbox\nmetadata:\n  name: %s\nspec:\n  image: %s\n  tier: ephemeral\n  policy: %s\n  lifecycle:\n    autoStop: 5m\n", name, image, policy)
	if err := os.WriteFile(manifest, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := fe.run(t, 120*time.Second, "cella", "apply", "-f", manifest); err != nil {
		t.Fatalf("cella apply (model_access policy %q): %v\n%s", policy, err, errOut)
	}
	t.Cleanup(func() { _, _, _ = fe.run(t, 60*time.Second, "cella", "delete", name) })

	// In-sandbox code: base URL must point at lux, the key the code holds must
	// be a placeholder (not a real lux_ key), and the call must still complete.
	script := fmt.Sprintf(`set -e
test -n "$ANTHROPIC_BASE_URL" || { echo "MISSING ANTHROPIC_BASE_URL"; exit 3; }
case "$ANTHROPIC_API_KEY" in lux_*) echo "LEAK: code holds a real lux key"; exit 4;; esac
curl -sS "$ANTHROPIC_BASE_URL/v1/messages" \
  -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"reply with the single word: ok"}]}'`, model)

	out, errOut, err := fe.run(t, 150*time.Second, "cella", "run", name, "--follow", "--", "sh", "-c", script)
	if err != nil {
		t.Fatalf("in-sandbox->lux call: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	if !strings.Contains(out, "content") && !strings.Contains(strings.ToLower(out), "ok") {
		t.Errorf("in-sandbox lux response lacked a completion:\n%s", out)
	}
}

// runLogoutTier proves server-side revocation (if-11): after logout, the
// previously valid bearer no longer verifies. Destructive: it ends the
// session, so it must be the last thing that runs.
func (fe *familyEnv) runLogoutTier(t *testing.T) {
	t.Run("if-11/logout-revokes", func(t *testing.T) {
		old := fe.token
		if _, errOut, err := fe.run(t, 20*time.Second, "logout"); err != nil {
			t.Fatalf("logout: %v\n%s", err, errOut)
		}
		status, _ := fe.get(t, fe.cellaURL+"/v1/sandboxes", old)
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("reused bearer after logout = %d, want 401/403 (if-11 revocation)", status)
		}
	})
}
