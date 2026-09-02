// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// The session verbs live at the top level (specs/004-flatten-auth-commands.md);
// `auth` survives only as a hidden back-compat alias.

func TestTopLevelSessionVerbsRegisteredInRoot(t *testing.T) {
	want := map[string]bool{
		"login": false, "logout": false, "whoami": false,
		"print-token": false, "org": false,
	}
	var authCmd *cobra.Command
	for _, c := range NewRoot("test").Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
		if c.Name() == "auth" {
			authCmd = c
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("top-level verb %q not registered in root", name)
		}
	}
	if authCmd == nil {
		t.Fatal("'auth' back-compat alias not registered in root")
	}
	if !authCmd.Hidden {
		t.Error("'auth' alias must be hidden")
	}
}

func TestAuthAliasHiddenButFunctional(t *testing.T) {
	// The alias resolves and its children carry the same flags as the
	// top-level verbs (same factories).
	got, err := executeForHelp(NewRoot("test"), "auth", "login", "--help")
	if err != nil {
		t.Fatalf("latere auth login --help: %v", err)
	}
	for _, w := range []string{"--token", "--no-git", "--org-id"} {
		if !strings.Contains(got, w) {
			t.Errorf("auth login help missing %q", w)
		}
	}
	if aliasHelp, err := executeForHelp(NewRoot("test"), "auth", "org", "switch", "--help"); err != nil {
		t.Fatalf("latere auth org switch --help: %v", err)
	} else if !strings.Contains(aliasHelp, "--personal") {
		t.Error("auth org switch help missing --personal")
	}

	// Root help lists the flat verbs and hides the alias.
	rootHelp, err := executeForHelp(NewRoot("test"), "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"login", "logout", "whoami", "print-token", "org"} {
		if !strings.Contains(rootHelp, w) {
			t.Errorf("root help missing top-level verb %q\n%s", w, rootHelp)
		}
	}
	for line := range strings.SplitSeq(rootHelp, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "auth ") {
			t.Errorf("root help still lists the auth group: %q", line)
		}
	}
}

func TestOrgShowsActiveContext(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"org context", map[string]any{"sub": "u1", "org_id": "org-123"}, "org-123"},
		{"personal context", map[string]any{"sub": "u1"}, "personal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LATERE_TOKEN_FILE", "/nonexistent/token.json")
			writeAuthTokenFile(t, fakeJWT(t, tc.claims), "refresh", time.Now().Add(time.Hour))

			cmd := newOrgCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(out.String()); got != tc.want {
				t.Errorf("latere org = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOrgShowNotLoggedIn(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", "/nonexistent/token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", "/nonexistent/auth-token.json")

	cmd := newOrgCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("latere org with no saved token: want error, got nil")
	}
}

func TestOrgSwitch(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantOrg string
	}{
		{"switch to org", []string{"org-456"}, "org-456"},
		{"switch to personal", []string{"--personal"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeAuthTokenFile(t, fakeJWT(t, map[string]any{"sub": "u1"}), "refresh-1", time.Now().Add(time.Hour))

			var gotOrg *string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/token" {
					http.NotFound(w, r)
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				v := r.PostForm.Get("org_id")
				gotOrg = &v
				if g := r.PostForm.Get("grant_type"); g != "refresh_token" {
					t.Errorf("grant_type = %q", g)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "new-access",
					"refresh_token": "new-refresh",
					"expires_in":    3600,
				})
			}))
			defer srv.Close()

			cmd := newOrgCmd()
			var errBuf bytes.Buffer
			cmd.SetOut(new(bytes.Buffer))
			cmd.SetErr(&errBuf)
			cmd.SetArgs(append([]string{"--auth-url", srv.URL}, tc.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if gotOrg == nil || *gotOrg != tc.wantOrg {
				t.Errorf("token endpoint got org_id %v, want %q", gotOrg, tc.wantOrg)
			}
			if !strings.Contains(errBuf.String(), "Switched to") {
				t.Errorf("missing switch confirmation, got %q", errBuf.String())
			}

			// The re-scoped token is persisted where the rest of the CLI reads it.
			b, err := os.ReadFile(os.Getenv("LATERE_AUTH_TOKEN_FILE"))
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(b, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.AccessToken != "new-access" {
				t.Errorf("saved access token = %q, want new-access", saved.AccessToken)
			}
		})
	}
}

func TestOrgPersonalAndArgMutuallyExclusive(t *testing.T) {
	cmd := newOrgCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"org-123", "--personal"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutually-exclusive error, got %v", err)
	}
}

func TestSkipUpdateCheckForPrintToken(t *testing.T) {
	root := NewRoot("test")
	cmd, _, err := root.Find([]string{"print-token"})
	if err != nil {
		t.Fatal(err)
	}
	if !skipUpdateCheck(cmd) {
		t.Error("print-token must skip the update check (bare-token stdout)")
	}
	aliased, _, err := root.Find([]string{"auth", "print-token"})
	if err != nil {
		t.Fatal(err)
	}
	if !skipUpdateCheck(aliased) {
		t.Error("auth print-token must skip the update check")
	}
}
