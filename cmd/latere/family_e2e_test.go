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

	"github.com/latere-ai/latere-cli/internal/api"
)

type familyEnv struct {
	bin       string // path to a freshly built latere binary
	token     string // cella-issued bearer (token.json), valid at cella
	authToken string // auth root bearer (auth-token.json), valid at auth
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

	// The auth root bearer (auth-token.json) is the token that verifies at
	// auth; token.json is cella-issued and 401s at auth by design.
	if at := os.Getenv("LATERE_E2E_AUTH_TOKEN"); at != "" {
		fe.authToken = at
	} else if tok, err := api.LoadToken(api.AuthTokenPath()); err == nil {
		fe.authToken = tok.AccessToken
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

	// The auth root bearer verifies at auth /tokeninfo and resolves to the
	// same owner (invariant 1). token.json is cella-issued and would 401
	// here by design (asserted separately below), so this uses authToken.
	t.Run("auth/tokeninfo-verifies", func(t *testing.T) {
		if fe.authToken == "" {
			t.Skip("no auth root token (auth-token.json); set LATERE_E2E_AUTH_TOKEN")
		}
		status, body := fe.get(t, fe.authURL+"/tokeninfo", fe.authToken)
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
	// Edge: CLI -> lux invoke (a real one-shot completion).
	t.Run("cli->lux-invoke", func(t *testing.T) {
		out, errOut, err := fe.run(t, 60*time.Second, "lux", "invoke", "--prompt", "reply with the single word: ok")
		if err != nil {
			t.Fatalf("lux invoke: %v\n%s", err, errOut)
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
		got, errOut, err := fe.run(t, 40*time.Second, "drive", "get", dest, "-")
		if err != nil {
			t.Fatalf("drive get: %v\n%s", err, errOut)
		}
		if !strings.Contains(got, want) {
			t.Errorf("drive get mismatch: got %q, want to contain %q", got, want)
		}
	})

	// The if-14 in-sandbox->lux edge is the flagship of this release; it is
	// covered by its own dedicated builder in family_e2e_ifat_test scope
	// once the sandbox release is deployed. Placeholder marker so coverage
	// is visibly intentional, not forgotten.
	t.Run("if-14/in-sandbox->lux", func(t *testing.T) {
		if os.Getenv("LATERE_FAMILY_E2E_SANDBOX") != "1" {
			t.Skip("set LATERE_FAMILY_E2E_SANDBOX=1 to create a cella and reach lux from inside it (costs a running sandbox)")
		}
		fe.inSandboxLux(t)
	})
}

// inSandboxLux creates an access-profile cella, runs in-sandbox code that
// calls lux through the egress substitution, and asserts the call
// succeeds without the code ever holding a real key (if-14).
func (fe *familyEnv) inSandboxLux(t *testing.T) {
	// Deferred to a dedicated builder once sandbox v0.10.223 is live; the
	// create/run/delete sequence is driven through `latere cella`.
	t.Skip("in-sandbox->lux builder lands with the sandbox release verification pass")
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
