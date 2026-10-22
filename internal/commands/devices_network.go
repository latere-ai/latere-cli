// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	goruntime "runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"latere.ai/x/cella/device"
	"latere.ai/x/cella/runtime/local"
)

// What sessions on this computer may reach, as `latere devices policy`
// changes it, and the reading of the host policy file the commands share.

// olderLatereNote is said whenever this command leaves the network any in
// the host policy file: a latere from before the network any refuses the
// file, so whoever runs one on this computer upgrades it.
const olderLatereNote = "Note: a latere older than this one cannot use a host policy file whose network is any, " +
	"and refuses it with device_policy_invalid. Upgrade it with `latere upgrade`."

// networkFlags are the network flags of `latere devices policy`: the mode,
// the hosts and the blocked hosts, each with whether it was given.
type networkFlags struct {
	mode                          string
	hosts, blocked                []string
	modeSet, hostsSet, blockedSet bool
}

func (f networkFlags) changed() bool { return f.modeSet || f.hostsSet || f.blockedSet }

// apply is n with the flags applied. A flag left out keeps what n says
// when the mode stays; a switch of mode starts each list from what the new
// mode reads of n: an allowlist from the git and package hosts, any from
// the entries of n that name a port beyond 443 and 80, since any already
// reaches every public host on those two.
func (f networkFlags) apply(n device.Network) (device.Network, error) {
	mode := n.Mode
	if f.modeSet {
		mode = f.mode
	}
	out := device.Network{Mode: mode}
	switch mode {
	case device.NetworkNone:
		if f.hostsSet || f.blockedSet {
			return device.Network{}, device.Fail(device.CodePolicyInvalid, "--host and --block name hosts, and the network none reaches no host", nil)
		}
	case device.NetworkAllowlist:
		if f.blockedSet {
			return device.Network{}, device.Fail(device.CodePolicyInvalid, "--block names the hosts the network any refuses, and the network is allowlist; add --network any", nil)
		}
		switch {
		case f.hostsSet:
			out.Hosts = f.hosts
		case n.Mode == device.NetworkAllowlist:
			out.Hosts = n.Hosts
		default:
			out.Hosts = slices.Clone(device.DefaultHosts)
		}
	case device.NetworkAny:
		switch {
		case f.hostsSet:
			out.Hosts = f.hosts
		case n.Mode == device.NetworkAny:
			out.Hosts = n.Hosts
		default:
			for _, h := range n.Hosts {
				if _, _, err := net.SplitHostPort(h); err == nil {
					out.Hosts = append(out.Hosts, h)
				}
			}
		}
		switch {
		case f.blockedSet:
			out.Blocked = f.blocked
		case n.Mode == device.NetworkAny:
			out.Blocked = n.Blocked
		}
	default:
		said := "network.mode"
		if f.modeSet {
			said = "--network"
		}
		return device.Network{}, device.Fail(device.CodePolicyInvalid, fmt.Sprintf("%s is %q; it is allowlist, none or any", said, mode), nil)
	}
	return out, nil
}

// nonEmpty is a flag's values trimmed, without the empty ones.
func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// changeNetwork writes the network the flags ask for into the host policy
// file at path, once it is checked as the engine checks a policy before it
// serves and, for the network any, once the person typed yes to what it
// allows (or passed --yes). Nothing is written when either fails.
func changeNetwork(cmd *cobra.Command, paths device.Paths, policy *device.Policy, f networkFlags, yes bool) error {
	n, err := f.apply(policy.Network)
	if err != nil {
		return err
	}
	next := *policy
	next.Network = n
	if err = policyRefusal(next, paths, ""); err != nil {
		return err
	}
	errOut := cmd.ErrOrStderr()
	if f.modeSet && n.Mode == device.NetworkAny {
		if err = confirmAny(cmd, next, yes); err != nil {
			return err
		}
	}
	if err = device.SavePolicy(paths.PolicyFile(), next); err != nil {
		return device.Fail(device.CodePolicyInvalid, "write the host policy file", err)
	}
	*policy = next
	fprintf(errOut, "Saved the network %s; it applies from the next `latere devices connect`.\n", n.Mode)
	if n.Mode == device.NetworkAny {
		fprintln(errOut, olderLatereNote)
	}
	return nil
}

// confirmAny shows what the network any allows, in the connect screen's
// own words, the sentence that everything readable in a granted folder can
// be sent to any public host among them, and takes the same typed yes the
// connect screen takes, unless yes was passed.
func confirmAny(cmd *cobra.Command, p device.Policy, yes bool) error {
	errOut := cmd.ErrOrStderr()
	fprint(errOut, networkScreen(p))
	if yes {
		return nil
	}
	fprint(errOut, "\nType yes to set this computer's network to any: ")
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return cliRefusal(codeNetworkNotConfirmed, "read the answer", err)
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		fprintln(errOut)
		return cliRefusal(codeNetworkNotConfirmed, fmt.Sprintf("the answer was %q", strings.TrimSpace(answer)), nil)
	}
	return nil
}

// networkScreen is the network of p as the connect screen states it: the
// mode and the hosts it reaches or blocks, and what it lets out.
func networkScreen(p device.Policy) string {
	var b strings.Builder
	for _, f := range device.ConnectFacts(p) {
		if f.Kind != device.FactNetwork && f.Kind != device.FactNetworkPorts {
			continue
		}
		b.WriteString(f.Text + "\n")
		if len(f.Items) > 0 {
			b.WriteString("  " + strings.Join(f.Items, ", ") + "\n")
		}
		if f.Note != "" {
			b.WriteString(f.Note + "\n")
		}
	}
	return b.String()
}

// policyRefusal is what is wrong with p, checked as the engine checks a
// policy before it serves: its own shape, then the local driver's folder,
// read path and network rules. The network any where this computer cannot
// enforce it is device_network_unenforceable, anything else
// device_policy_invalid, and a policy with nothing wrong nil. prefix opens
// the detail.
func policyRefusal(p device.Policy, paths device.Paths, prefix string) error {
	problems := p.Problems(paths.Home)
	code := device.CodePolicyInvalid
	if len(problems) == 0 {
		if err := device.CheckDriver(p, paths, goruntime.GOOS, sessionDriver); err != nil {
			problems = append(problems, err.Error())
			if errors.Is(err, local.ErrNetworkUnenforceable) {
				code = device.CodeNetworkUnenforceable
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return device.Fail(code, prefix+strings.Join(problems, "; "), nil)
}

// loadPolicy reads the host policy file at path as device.LoadPolicy does,
// with its refusal as the CLI prints it (loadRefusal).
func loadPolicy(path string) (device.Policy, bool, error) {
	p, ok, err := device.LoadPolicy(path)
	if err != nil {
		return device.Policy{}, ok, loadRefusal(err)
	}
	return p, ok, nil
}

// loadRefusal is the refusal of a host policy file that did not read:
// device_policy_newer for one naming a field this version does not know,
// which a newer latere or Latere Studio wrote, so the step is an upgrade;
// device_policy_invalid for anything else.
func loadRefusal(err error) error {
	if newerPolicy(err) {
		return newerRefusal(err)
	}
	return device.Fail(device.CodePolicyInvalid, "", err)
}

// newerRefusal is device_policy_newer for err, a host policy file naming a
// field this version does not know, with the detail of the engine's own
// refusal when err is one.
func newerRefusal(err error) error {
	var de *device.Error
	if errors.As(err, &de) {
		return cliRefusal(codePolicyNewer, de.Detail, de.Err)
	}
	return cliRefusal(codePolicyNewer, "", err)
}

// newerPolicy reports whether err is a host policy file that names a field
// this version does not know.
func newerPolicy(err error) bool {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return false
	}
	return slices.ContainsFunc(te.Errors, func(e string) bool { return strings.Contains(e, " not found in type ") })
}
