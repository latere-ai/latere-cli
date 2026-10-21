// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin

package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"latere.ai/x/cella/device"
	"latere.ai/x/cella/runtime"
	"latere.ai/x/cella/runtime/local"
)

// On macOS the engine `latere devices` runs denies every session Latere
// Studio's data and cache, as the app's own engine does, beside the
// engine's own paths; and the policy check builds the driver with them.
func TestServedSessionsAreDeniedStudiosFolders(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	newDevicePlatform(t)
	signIn(t, "")
	connectedHere(t, paths, project)
	studio := []string{
		filepath.Join(paths.Home, "Library", "Application Support", "ai.latere.studio"),
		filepath.Join(paths.Home, "Library", "Caches", "ai.latere.studio"),
	}
	var (
		mu    sync.Mutex
		built []local.Options
	)
	build := deviceDriver
	deviceDriver = func(o local.Options) (runtime.Driver, error) {
		mu.Lock()
		built = append(built, o)
		mu.Unlock()
		return build(o)
	}
	t.Cleanup(func() { deviceDriver = build })

	if _, errOut, err := runDevices(t, "", "devices", "connect"); device.CodeOf(err) != device.CodeWorkerReplaced {
		t.Fatalf("connect returned %v:\n%s", err, errOut)
	}
	mu.Lock()
	served := slices.Clone(built)
	mu.Unlock()
	if len(served) == 0 {
		t.Fatal("the engine built no driver")
	}
	for _, o := range served {
		for _, want := range append([]string{paths.Config, paths.State}, studio...) {
			if !slices.Contains(o.Deny, want) {
				t.Errorf("the engine's driver does not deny %s: %v", want, o.Deny)
			}
		}
	}

	// A read path inside the app's data is refused by the driver the check
	// builds, which holds the same denied paths.
	if err := os.MkdirAll(studio[0], 0o700); err != nil {
		t.Fatal(err)
	}
	policy, _, err := device.LoadPolicy(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	policy.ReadPaths = []string{"~/Library/Application Support/ai.latere.studio"}
	if err = device.SavePolicy(paths.PolicyFile(), policy); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runDevices(t, "", "devices", "policy"); device.CodeOf(err) != device.CodePolicyInvalid || !strings.Contains(err.Error(), "deny table") {
		t.Errorf("a read path in Latere Studio's data returned %v", err)
	}
}
