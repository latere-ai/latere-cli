// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestServeUpgrade(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	previous := latestRelease
	t.Cleanup(func() { latestRelease = previous })
	latestRelease = func(context.Context, *http.Client) (string, error) { return "v0.30.0", nil }
	if latest, ok := serveUpgrade("v0.29.0")(context.Background()); !ok || latest != "v0.30.0" {
		t.Errorf("a newer release reads %q, %v", latest, ok)
	}
	if _, ok := serveUpgrade("v0.30.0")(context.Background()); ok {
		t.Error("the running release reads as an upgrade")
	}
	latestRelease = func(context.Context, *http.Client) (string, error) { return "", errors.New("offline") }
	if _, ok := serveUpgrade("v0.29.0")(context.Background()); ok {
		t.Error("a failed lookup reads as an upgrade")
	}
}

// A copy a desktop application bundles runs with LATERE_NO_UPDATE_CHECK, and
// a long serve then asks for no release at all, as CI does.
func TestServeHonorsNoUpdateCheck(t *testing.T) {
	previous := latestRelease
	t.Cleanup(func() { latestRelease = previous })
	asked := false
	latestRelease = func(context.Context, *http.Client) (string, error) { asked = true; return "v9.0.0", nil }
	for _, env := range []string{"LATERE_NO_UPDATE_CHECK", "CI"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("LATERE_NO_UPDATE_CHECK", "")
			t.Setenv("CI", "")
			t.Setenv(env, "1")
			if check := serveUpgrade("v0.29.0"); check != nil {
				t.Errorf("under %s serve still asks for a release", env)
			}
		})
	}
	if asked {
		t.Error("a release was looked up")
	}
}
