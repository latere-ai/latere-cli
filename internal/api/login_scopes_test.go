// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"strings"
	"testing"
)

// The CLI must not request sandbox scopes from auth. Cella decides what a
// bearer may do from its own state, so a *:sandbox grant here would be
// inert vocabulary that auth still has to keep issuing.
//
// Pinned as an exact set rather than a "does not contain" check: the failure
// this guards against is someone re-adding a scope while widening the list for
// an unrelated reason, and an exact assertion makes that visible in the diff.
func TestLoginScopesRequestsNoSandboxScopes(t *testing.T) {
	got := strings.Fields(LoginScopes)
	want := []string{
		"openid", "email", "profile", "offline_access",
		"run:agents", "read:agents", "write:agents",
	}
	if len(got) != len(want) {
		t.Fatalf("LoginScopes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LoginScopes = %v, want %v", got, want)
		}
	}
	for _, s := range got {
		if strings.HasSuffix(s, ":sandbox") || s == "policy:write" {
			t.Errorf("LoginScopes requests %q; Cella does not read auth-issued sandbox scopes", s)
		}
	}
}
