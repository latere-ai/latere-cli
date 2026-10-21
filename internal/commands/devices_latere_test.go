// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"latere.ai/x/cella/device"
)

// readDeviceToken reads the claims the engine checks a handed token by,
// as the platform's issuer writes them.
func TestReadDeviceToken(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   device.TokenClaims
	}{
		{"aud as a string", map[string]any{"sub": "p1", "aud": "cella", "exp": exp.Unix()},
			device.TokenClaims{Audiences: []string{"cella"}, Subject: "p1", Expiry: exp, Personal: true}},
		{"aud as a list", map[string]any{"sub": "p1", "aud": []string{"api.latere.ai", "cella"}, "exp": exp.Unix()},
			device.TokenClaims{Audiences: []string{"api.latere.ai", "cella"}, Subject: "p1", Expiry: exp, Personal: true}},
		{"no exp", map[string]any{"sub": "p1", "aud": "cella"},
			device.TokenClaims{Audiences: []string{"cella"}, Subject: "p1", Personal: true}},
		{"an exp of zero", map[string]any{"sub": "p1", "aud": "cella", "exp": 0},
			device.TokenClaims{Audiences: []string{"cella"}, Subject: "p1", Personal: true}},
		{"an organization's context", map[string]any{"sub": "p1", "aud": "cella", "exp": exp.Unix(), "org_id": "org-1"},
			device.TokenClaims{Audiences: []string{"cella"}, Subject: "p1", Expiry: exp, Context: "org-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readDeviceToken(fakeJWT(t, tc.claims))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Audiences, tc.want.Audiences) || got.Subject != tc.want.Subject || !got.Expiry.Equal(tc.want.Expiry) ||
				got.Personal != tc.want.Personal || got.Context != tc.want.Context {
				t.Errorf("readDeviceToken = %+v, want %+v", got, tc.want)
			}
		})
	}
	for name, token := range map[string]string{
		"not a JWT":       "not-a-token",
		"claims not JSON": "e30.bm90IGpzb24.sig",
		"no audience":     fakeJWT(t, map[string]any{"sub": "p1"}),
		"aud not names":   fakeJWT(t, map[string]any{"sub": "p1", "aud": []int{1}}),
	} {
		if _, err := readDeviceToken(token); err == nil {
			t.Errorf("%s: readDeviceToken took it", name)
		}
	}
}

// The command's own refusals read as the engine's do: the code to branch on,
// the fixed sentence and the next step, and the detail.
func TestCLIRefusals(t *testing.T) {
	for code, want := range map[string]string{
		codeUnverifiedBuild: "cannot verify its own upgrades",
		codeLogoutCancelled: "you are still signed in, and this computer is still connected",
		codeLogoutBlocked:   "could not be removed from your devices",
	} {
		cause := errors.New("the cause")
		err := cliRefusal(code, "", cause)
		if device.CodeOf(err) != code || !strings.Contains(err.Message, want) || err.Detail != "the cause" || !errors.Is(err, cause) {
			t.Errorf("%s reads %+v", code, err)
		}
		if !strings.HasPrefix(err.Error(), code+": ") {
			t.Errorf("%s renders %q", code, err.Error())
		}
	}
	if hint := cliRefusal(codeLogoutBlocked, "", nil).Hint; !strings.Contains(hint, "latere devices remove") {
		t.Errorf("the blocked sign-out's next step is %q", hint)
	}
}
