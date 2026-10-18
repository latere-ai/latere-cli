// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/devices"
)

// TestDeviceAccount holds the engine's Account to the answers Cella and the
// platform give: a key minted with its lifetime, a revocation, and an
// environment that is gone, removed, or still the person's.
func TestDeviceAccount(t *testing.T) {
	answers := map[string]struct {
		status int
		body   string
	}{
		"dev-live":    {http.StatusOK, `{"kind":"Environment","metadata":{"name":"dev-live"}}`},
		"dev-gone":    {http.StatusNotFound, `{"error":{"code":"not_found","message":"There is no such object."}}`},
		"dev-removed": {http.StatusForbidden, `{"error":{"code":"forbidden","message":"no","details":{"detail":"forbidden: environment.read: device_removed"}}}`},
		"dev-other":   {http.StatusForbidden, `{"error":{"code":"forbidden","message":"no","details":{"detail":"forbidden: environment.read: device_not_owned"}}}`},
		"dev-down":    {http.StatusServiceUnavailable, `{"error":{"code":"authorizer_unavailable","message":"retry"}}`},
	}
	var revoked []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/environments/environments/{id}", func(w http.ResponseWriter, r *http.Request) {
		a := answers[r.PathValue("id")]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = fmt.Fprint(w, a.body)
	})
	mux.HandleFunc("POST /v1/environments/environments/{id}/keys", func(w http.ResponseWriter, r *http.Request) {
		a := answers[r.PathValue("id")]
		w.Header().Set("Content-Type", "application/json")
		if a.status != http.StatusOK {
			w.WriteHeader(a.status)
			_, _ = fmt.Fprint(w, a.body)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"token":"t","jti":"j","exp":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("DELETE /v1/environments/environments/{id}/keys/{jti}", func(w http.ResponseWriter, r *http.Request) {
		revoked = append(revoked, r.PathValue("jti"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("LATERE_ENVIRONMENTS_TOKEN", "cella-bearer")
	account := deviceAccount{apiURL: srv.URL + "/v1/environments"}
	ctx := context.Background()

	key, err := account.MintKey(ctx, "dev-live", devices.KeyTTL)
	if err != nil || key.Token != "t" || key.JTI != "j" || key.Environment != "dev-live" || key.ExpiresAt.IsZero() {
		t.Errorf("the mint answered %+v, %v", key, err)
	}
	for _, id := range []string{"dev-gone", "dev-removed", "dev-other"} {
		if _, err := account.MintKey(ctx, id, devices.KeyTTL); !errors.Is(err, devices.ErrRemoved) {
			t.Errorf("a mint for %s answered %v, want ErrRemoved", id, err)
		}
	}
	if _, err := account.MintKey(ctx, "dev-down", devices.KeyTTL); err == nil || errors.Is(err, devices.ErrRemoved) {
		t.Errorf("a mint the authorizer could not decide answered %v", err)
	}
	if err := account.RevokeKey(ctx, "dev-live", "jti-old"); err != nil || len(revoked) != 1 || revoked[0] != "jti-old" {
		t.Errorf("the revocation answered %v, revoked %v", err, revoked)
	}
	for id, want := range map[string]bool{"dev-live": true, "dev-gone": false, "dev-removed": false, "dev-other": false} {
		if exists, err := account.Exists(ctx, id); err != nil || exists != want {
			t.Errorf("Exists(%s) = %v, %v; want %v", id, exists, err, want)
		}
	}
	if _, err := account.Exists(ctx, "dev-down"); err == nil {
		t.Error("an environment the authorizer could not decide reads as known")
	}

	t.Setenv("LATERE_ENVIRONMENTS_URL", "")
	broken := deviceAccount{apiURL: "not a url"}
	if _, err := broken.MintKey(ctx, "x", time.Hour); err == nil {
		t.Error("a mint against no URL succeeded")
	}
	if err := broken.RevokeKey(ctx, "x", "j"); err == nil {
		t.Error("a revocation against no URL succeeded")
	}
	if _, err := broken.Exists(ctx, "x"); err == nil {
		t.Error("a read against no URL succeeded")
	}
}

func TestUpgradeAvailable(t *testing.T) {
	previous := latestRelease
	t.Cleanup(func() { latestRelease = previous })
	latestRelease = func(context.Context, *http.Client) (string, error) { return "v0.30.0", nil }
	if latest, ok := upgradeAvailable("v0.29.0")(context.Background()); !ok || latest != "v0.30.0" {
		t.Errorf("a newer release reads %q, %v", latest, ok)
	}
	if _, ok := upgradeAvailable("v0.30.0")(context.Background()); ok {
		t.Error("the running release reads as an upgrade")
	}
	latestRelease = func(context.Context, *http.Client) (string, error) { return "", errors.New("offline") }
	if _, ok := upgradeAvailable("v0.29.0")(context.Background()); ok {
		t.Error("a failed lookup reads as an upgrade")
	}
}
