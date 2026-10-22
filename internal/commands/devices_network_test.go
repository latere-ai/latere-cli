// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"os"
	"slices"
	"strings"
	"testing"

	"latere.ai/x/cella/device"
	"latere.ai/x/cella/runtime"
	"latere.ai/x/cella/runtime/local"
)

// The network of `latere devices policy` and the hosts `latere devices
// allow` lets sessions reach.

// anyDriver is a driver that takes the network any, which the local driver
// takes only under macOS's own sandbox in a binary whose main calls
// hostsandbox.Main, never in this test binary. The policy check builds it
// and closes it; nothing else is asked of it.
type anyDriver struct{ runtime.Driver }

// acceptAny makes the policy check take the network any, building the
// local driver for every other network.
func acceptAny(t *testing.T) {
	t.Helper()
	build := deviceDriver
	deviceDriver = func(o local.Options) (runtime.Driver, error) {
		if o.Network.Mode == local.NetworkAny {
			return anyDriver{}, nil
		}
		return build(o)
	}
	t.Cleanup(func() { deviceDriver = build })
}

// policyNetwork is the network the host policy file names now.
func policyNetwork(t *testing.T, paths device.Paths) device.Network {
	t.Helper()
	p, _, err := device.LoadPolicy(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	return p.Network
}

// withNetwork writes n into the connected computer's host policy file.
func withNetwork(t *testing.T, paths device.Paths, n device.Network) {
	t.Helper()
	p, _, err := device.LoadPolicy(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	p.Network = n
	if err = device.SavePolicy(paths.PolicyFile(), p); err != nil {
		t.Fatal(err)
	}
}

const anyWarning = "Everything readable in a granted folder can be sent to any public host."

func TestDevicesPolicyNetworkFlags(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	connectedHere(t, paths, project)

	// The network none names no host, and a block belongs to any alone.
	for _, args := range [][]string{{"--host", "github.com"}, {"--block", "example.com"}, {"--network", "open"}} {
		_, _, err := runDevices(t, "", append([]string{"devices", "policy"}, args...)...)
		if device.CodeOf(err) != device.CodePolicyInvalid {
			t.Errorf("policy %v under none returned %v", args, err)
		}
	}
	if n := policyNetwork(t, paths); n.Mode != device.NetworkNone {
		t.Fatalf("a refused change wrote the network %+v", n)
	}

	// An allowlist starts from the git and package hosts, or the hosts named.
	out, errOut, err := runDevices(t, "", "devices", "policy", "--network", "allowlist")
	if err != nil {
		t.Fatalf("policy --network allowlist returned %v:\n%s", err, errOut)
	}
	if n := policyNetwork(t, paths); n.Mode != device.NetworkAllowlist || !slices.Equal(n.Hosts, device.DefaultHosts) {
		t.Errorf("--network allowlist wrote %+v", n)
	}
	if !strings.Contains(errOut, "Saved the network allowlist; it applies from the next `latere devices connect`.") ||
		!strings.Contains(out, "Network: commands may reach these hosts only:\n  github.com, codeload.github.com") ||
		!strings.Contains(out, "The host policy file is valid.") {
		t.Errorf("policy --network allowlist printed:\n%s\n%s", out, errOut)
	}
	if strings.Contains(errOut, "latere upgrade") {
		t.Errorf("an allowlist was said to need an upgrade:\n%s", errOut)
	}
	if _, _, err = runDevices(t, "", "devices", "policy", "--host", "github.com", "--host", "github.com:22"); err != nil {
		t.Fatal(err)
	}
	if n := policyNetwork(t, paths); !slices.Equal(n.Hosts, []string{"github.com", "github.com:22"}) {
		t.Errorf("--host wrote %+v", n)
	}
	if _, _, err = runDevices(t, "", "devices", "policy", "--block", "example.com"); device.CodeOf(err) != device.CodePolicyInvalid ||
		!strings.Contains(err.Error(), "add --network any") {
		t.Errorf("--block under an allowlist returned %v", err)
	}

	// This test binary, like any program that does not call
	// hostsandbox.Main and like every Linux computer, cannot enforce any:
	// it is refused with its own code before anything is asked or written.
	_, errOut, err = runDevices(t, "yes\n", "devices", "policy", "--network", "any")
	if device.CodeOf(err) != device.CodeNetworkUnenforceable || !strings.Contains(err.Error(), "Choose allowlist or none") {
		t.Errorf("--network any where it cannot be enforced returned %v", err)
	}
	if strings.Contains(errOut, "Type yes") {
		t.Errorf("an unenforceable any was put to the person:\n%s", errOut)
	}
	if n := policyNetwork(t, paths); n.Mode != device.NetworkAllowlist {
		t.Errorf("an unenforceable any wrote %+v", n)
	}
	// A file that says any here reads as unenforceable too.
	withNetwork(t, paths, device.Network{Mode: device.NetworkAny, Blocked: []string{"example.com"}})
	if _, _, err = runDevices(t, "", "devices", "policy"); device.CodeOf(err) != device.CodeNetworkUnenforceable {
		t.Errorf("policy over a file of any returned %v", err)
	}
}

func TestDevicesPolicyNetworkAnyAsksForYes(t *testing.T) {
	paths, project := deviceHome(t)
	sandboxStandIn(t)
	acceptAny(t)
	connectedHere(t, paths, project)
	withNetwork(t, paths, device.Network{Mode: device.NetworkAllowlist, Hosts: []string{"github.com", "github.com:22"}})

	for _, answer := range []string{"no\n", "y\n", ""} {
		_, errOut, err := runDevices(t, answer, "devices", "policy", "--network", "any", "--block", "internal.example.com")
		if device.CodeOf(err) != codeNetworkNotConfirmed || !strings.Contains(err.Error(), "The network was not changed.") {
			t.Fatalf("--network any answered %q returned %v", answer, err)
		}
		for _, want := range []string{"Network: any.", "any public host", "internal.example.com", anyWarning, "Type yes"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("the question lacks %q:\n%s", want, errOut)
			}
		}
		if n := policyNetwork(t, paths); n.Mode != device.NetworkAllowlist {
			t.Fatalf("any was written before a typed yes: %+v", n)
		}
	}

	out, errOut, err := runDevices(t, "yes\n", "devices", "policy", "--network", "any", "--block", "internal.example.com", "--block", "*.corp.example.com")
	if err != nil {
		t.Fatalf("--network any answered yes returned %v:\n%s", err, errOut)
	}
	// Any reaches every public host on 443 and 80, so of the allowlist only
	// the host on another port stays.
	n := policyNetwork(t, paths)
	if n.Mode != device.NetworkAny || !slices.Equal(n.Blocked, []string{"internal.example.com", "*.corp.example.com"}) ||
		!slices.Equal(n.Hosts, []string{"github.com:22"}) {
		t.Errorf("--network any wrote %+v", n)
	}
	raw, err := os.ReadFile(paths.PolicyFile())
	if err != nil || !strings.Contains(string(raw), "mode: any") || !strings.Contains(string(raw), "blocked: [internal.example.com, ") {
		t.Errorf("the host policy file reads %s, %v", raw, err)
	}
	if !strings.Contains(errOut, "Saved the network any; it applies from the next `latere devices connect`.") ||
		!strings.Contains(errOut, "a latere older than this one cannot use a host policy file whose network is any") ||
		!strings.Contains(errOut, "`latere upgrade`") {
		t.Errorf("--network any said:\n%s", errOut)
	}
	// The policy's own output says the mode, what it blocks, what it lets
	// out, and the ports beyond 443 and 80.
	for _, want := range []string{
		"Network: any. Commands on this computer may reach any public host", "  internal.example.com, *.corp.example.com\n" + anyWarning,
		"on the port named:\n  github.com:22", "The host policy file is valid.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the policy lacks %q:\n%s", want, out)
		}
	}

	// A narrower block list asks nothing; --block names the whole list.
	if _, errOut, err = runDevices(t, "", "devices", "policy", "--block", "internal.example.com"); err != nil || strings.Contains(errOut, "Type yes") {
		t.Fatalf("--block under any returned %v:\n%s", err, errOut)
	}
	if n = policyNetwork(t, paths); n.Mode != device.NetworkAny || !slices.Equal(n.Blocked, []string{"internal.example.com"}) {
		t.Errorf("--block wrote %+v", n)
	}
	// Under any a host names a port beyond 443 and 80.
	if _, _, err = runDevices(t, "", "devices", "policy", "--host", "db.example.com"); device.CodeOf(err) != device.CodePolicyInvalid ||
		!strings.Contains(err.Error(), "names a port beyond 443 and 80") {
		t.Errorf("a host with no port under any returned %v", err)
	}
	// --yes answers for the person, who is still told what any allows.
	withNetwork(t, paths, device.Network{Mode: device.NetworkNone})
	if _, errOut, err = runDevices(t, "", "devices", "policy", "--network", "any", "--yes"); err != nil ||
		!strings.Contains(errOut, anyWarning) || strings.Contains(errOut, "Type yes") {
		t.Fatalf("--network any --yes returned %v:\n%s", err, errOut)
	}
	if n = policyNetwork(t, paths); n.Mode != device.NetworkAny || len(n.Blocked) != 0 || len(n.Hosts) != 0 {
		t.Errorf("--network any --yes wrote %+v", n)
	}
	// Back to an allowlist: the block list goes, and the git and package
	// hosts come back.
	if _, _, err = runDevices(t, "", "devices", "policy", "--network", "allowlist"); err != nil {
		t.Fatal(err)
	}
	if n = policyNetwork(t, paths); n.Mode != device.NetworkAllowlist || len(n.Blocked) != 0 || !slices.Equal(n.Hosts, device.DefaultHosts) {
		t.Errorf("--network allowlist after any wrote %+v", n)
	}
}

// A connect never proposes any: the flag is refused with the command that
// sets it once connected, before anything reaches the platform.
func TestConnectRefusesNetworkAny(t *testing.T) {
	paths, project := deviceHome(t)
	p := newDevicePlatform(t)
	signIn(t, "")
	_, _, err := runDevices(t, "yes\n", "devices", "connect", "--folder", project, "--network", "any")
	if device.CodeOf(err) != device.CodePolicyInvalid || !strings.Contains(err.Error(), "latere devices policy --network any") {
		t.Errorf("connect --network any returned %v", err)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("connect --network any reached the platform: %v", got)
	}
	if _, err := os.Stat(paths.PolicyFile()); err == nil {
		t.Error("connect --network any wrote the host policy file")
	}
}

// A file that names a field this version does not know was written by a
// newer latere or Latere Studio, and the step is to upgrade.
func TestDevicesNewerPolicyFileSaysUpgrade(t *testing.T) {
	paths, project := deviceHome(t)
	connectedHere(t, paths, project)
	raw, err := os.ReadFile(paths.PolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(string(raw), "network:\n", "network:\n  proxy: socks5://127.0.0.1:1080\n", 1)
	if newer == string(raw) {
		t.Fatalf("the file has no network to add a field to:\n%s", raw)
	}
	if err = os.WriteFile(paths.PolicyFile(), []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"devices", "policy"}, {"devices", "activity"}, {"devices", "connect"}, {"devices", "allow", "example.com", "--save"}} {
		_, _, err := runDevices(t, "", args...)
		if device.CodeOf(err) != codePolicyNewer || !strings.Contains(err.Error(), "Upgrade with `latere upgrade`") || !strings.Contains(err.Error(), "proxy") {
			t.Errorf("%v over a newer file returned %v", args, err)
		}
	}
	// A file that does not read for another reason is still invalid.
	if err = os.WriteFile(paths.PolicyFile(), []byte("folders: {\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runDevices(t, "", "devices", "policy"); device.CodeOf(err) != device.CodePolicyInvalid {
		t.Errorf("policy over a broken file returned %v", err)
	}
}
