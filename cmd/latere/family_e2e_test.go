// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

// Family production E2E: drives the whole Latere product family through
// one `latere` CLI identity and asserts each identity-fabric edge end to
// end against live production. It is the reproducible companion to
// specs/products/identity-fabric/release-and-verification.md: one login,
// then every edge a CLI user can reach (cella, models, drive, topos, auth),
// plus the two invariants (owner-rooted subject, trust-root rule).
//
// Opt-in and tiered, because the higher tiers spend real money and mutate
// real state:
//
//	LATERE_FAMILY_E2E=1        read-only edges: whoami, /api/me,
//	                           cella list, models list, drive ls,
//	                           topos reachability, garbage-token 401.
//	                           No cost, no resource creation.
//	LATERE_FAMILY_E2E_WRITE=1  also: models invoke (a token), drive put/get/rm
//	                           round-trip, cross-product 401. Spends money;
//	                           cleans up after itself.
//	LATERE_FAMILY_E2E_LOGOUT=1 also: logout then reuse the old bearer ->
//	                           401. Destructive: ends the session.
//
// Identity comes from the logged-in CLI (~/.config/latere/token.json) or
// LATERE_E2E_TOKEN. Run:
//
//	LATERE_FAMILY_E2E=1 go test ./cmd/latere/ -run TestFamilyE2E -v
//
// Service URLs default to production and are overridable:
// CELLA_API_URL, AUTH_URL, LATERE_MODELS_URL, DRIVE_API_URL, TOPOS_API_URL.
// The models edges create this machine's model key on first use, as any
// signed-in `latere models` does.

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
	bin      string // path to a freshly built latere binary
	token    string // cella-issued bearer (token.json), valid at cella
	cellaURL string
	authURL  string
	driveURL string
	toposURL string
	sub      string // owner subject, read from whoami
	httpc    *http.Client
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
		cellaURL: urlOr("CELLA_API_URL", "https://api.latere.ai/v1/environments"),
		authURL:  urlOr("AUTH_URL", "https://auth.latere.ai"),
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

// freshBearer is the saved login token after `whoami` refreshed it when due,
// the token auth's /api/me takes. Unlike LATERE_E2E_TOKEN or a login left
// from an earlier run, it is current. Returns "" if either command fails.
func (fe *familyEnv) freshBearer(t *testing.T) string {
	t.Helper()
	if _, _, err := fe.run(t, 30*time.Second, "whoami"); err != nil {
		return ""
	}
	out, _, err := fe.run(t, 20*time.Second, "print-token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// firstModel reads the first model `latere models` lists, so a live invoke
// uses a model the key reaches.
func (fe *familyEnv) firstModel(t *testing.T) string {
	t.Helper()
	out, _, err := fe.run(t, 30*time.Second, "models")
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(first)
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
		for line := range strings.SplitSeq(out, "\n") {
			if after, ok := strings.CutPrefix(strings.TrimSpace(line), "sub:"); ok {
				fe.sub = strings.TrimSpace(after)
			}
		}
		if fe.sub == "" {
			t.Fatal("could not read owner sub from whoami")
		}
	})

	// A current auth-issued bearer is accepted at auth /api/me and resolves
	// to the same owner (invariant 1).
	t.Run("auth/api-me-accepts", func(t *testing.T) {
		bearer := fe.freshBearer(t)
		if bearer == "" {
			t.Skip("could not read a current login token with whoami and print-token")
		}
		status, body := fe.get(t, fe.authURL+"/api/me", bearer)
		if status != http.StatusOK {
			t.Fatalf("/api/me = %d, want 200\n%s", status, body)
		}
		var info map[string]any
		if err := json.Unmarshal([]byte(body), &info); err != nil {
			t.Fatalf("/api/me not json: %v\n%s", err, body)
		}
		if fe.sub != "" {
			if got, _ := info["sub"].(string); got != "" && got != fe.sub {
				t.Errorf("/api/me sub = %q, want owner %q (invariant 1)", got, fe.sub)
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

	// Edge: CLI -> the models at the origin, with the model key.
	t.Run("cli->models-list", func(t *testing.T) {
		out, errOut, err := fe.run(t, 60*time.Second, "models")
		if err != nil {
			t.Fatalf("models: %v\n%s", err, errOut)
		}
		if strings.TrimSpace(out) == "" {
			t.Error("models returned nothing; expected the models the key reaches")
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
		for _, u := range []string{fe.authURL + "/tokeninfo", fe.cellaURL + "/sandboxes"} {
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

// runWriteTier exercises the cost/mutation edges: a live model completion and
// a drive round-trip. Each cleans up after itself.
func (fe *familyEnv) runWriteTier(t *testing.T) {
	// Edge: CLI -> models invoke (a real one-shot completion) with the first
	// model the key reaches.
	t.Run("cli->models-invoke", func(t *testing.T) {
		model := fe.firstModel(t)
		if model == "" {
			t.Skip("the model key reaches no model")
		}
		out, errOut, err := fe.run(t, 60*time.Second, "models", "invoke",
			"--model", model, "--max-tokens", "16",
			"reply with the single word: ok")
		if err != nil {
			t.Fatalf("models invoke (%s): %v\n%s", model, err, errOut)
		}
		if strings.TrimSpace(out) == "" {
			t.Error("models invoke returned empty completion")
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
}

// runLogoutTier proves server-side revocation: after logout, the
// previously valid bearer no longer verifies. Destructive: it ends the
// session, so it must be the last thing that runs.
func (fe *familyEnv) runLogoutTier(t *testing.T) {
	t.Run("logout-revokes", func(t *testing.T) {
		old := fe.token
		if _, errOut, err := fe.run(t, 20*time.Second, "logout"); err != nil {
			t.Fatalf("logout: %v\n%s", err, errOut)
		}
		status, _ := fe.get(t, fe.cellaURL+"/sandboxes", old)
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("reused bearer after logout = %d, want 401/403 (revocation)", status)
		}
	})
}
