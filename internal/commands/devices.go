// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/cella/device"
	"latere.ai/x/sdk-go/account"
)

// newDevicesCmd is the `latere devices ...` command tree (spec 016): this
// computer as one of the person's environments, which the sessions they
// start can work on, and the person's devices at the platform.
func newDevicesCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "Connect this computer so your sessions can work on it, and manage your devices.",
		Long: `Connect this computer to Latere, so the sessions you start can run
commands and edit files on it, in the folders you choose.

'latere devices connect' connects this computer and serves it in the
foreground. Sessions you start, and your triggers that run on a schedule,
start there without asking; a trigger that runs on mail or a webhook asks
for your approval there first, and 'latere devices policy --session-start
ask' makes every session ask. Each command that changes something is still
approved in the chat, unless you approved a matching one before.
'latere devices pause' stops every session on the computer at once.
The host policy file ~/.config/latere/device.yaml holds what sessions may
use, and the activity log holds every operation they asked for.

Devices run on macOS and Linux, need the sandbox runtime srt, and are
personal: connect a computer from your personal context.`,
		Example: `  latere devices connect --folder ~/code/site
  latere devices list
  latere devices activity -f
  latere devices pause
  latere devices remove`,
	}
	cmd.AddCommand(
		newDevicesConnectCmd(version),
		newDevicesServeCmd(version),
		newDevicesListCmd(),
		newDevicesRenameCmd(),
		newDevicesRemoveCmd(),
		newDevicesPolicyCmd(),
		newDevicesActivityCmd(),
		newDevicesControlCmd("pause", "/v1/pause",
			"Stop every session on this computer at once, until you resume it.",
			`Disconnect this computer while 'latere devices connect' or 'latere devices
serve' keeps running: every command a session started here stops, and no
session can use the computer until 'latere devices resume'.`,
			"Paused. Every command a session started on this computer was stopped, and no session can use it until `latere devices resume`."),
		newDevicesControlCmd("resume", "/v1/resume",
			"Let sessions use this computer again after a pause.",
			`Connect this computer again after 'latere devices pause', so sessions can
use it as before.`,
			"Resumed. Sessions can use this computer again."),
	)
	return cmd
}

// thisDevice is the device this computer is, from the host policy file, ""
// when it is not connected.
func thisDevice() (device.Paths, device.Policy, error) {
	paths, err := device.DefaultPaths()
	if err != nil {
		return device.Paths{}, device.Policy{}, device.Fail(device.CodeServeFailed, "resolve the configuration directory", err)
	}
	policy, _, err := loadPolicy(paths.PolicyFile())
	if err != nil {
		return paths, device.Policy{}, err
	}
	return paths, policy, nil
}

func newDevicesListCmd() *cobra.Command {
	var (
		platformURL string
		jsonF       bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List your devices, with this computer marked.",
		Long: `List the computers you connected: each one's name, id, operating system,
whether it is online, when it was last seen, and the folders it lets
sessions use. This computer is marked with *.`,
		Example: `  latere devices list
  latere devices list --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, policy, err := thisDevice()
			if err != nil {
				return err
			}
			list, err := (deviceAuth{platformURL: platformURL}).devices(cmd.Context())
			if err != nil {
				return wrapReposErr(err)
			}
			if jsonF {
				// The platform's own answer, with every field it sends.
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(list.RawJSON())
			}
			return printDevices(cmd.OutOrStdout(), list, policy.Device)
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// printDevices writes the list as a table.
func printDevices(out io.Writer, list *account.Devices, here string) error {
	var b bytes.Buffer
	if len(list.Items) == 0 {
		b.WriteString("You have no devices. Connect this computer with: latere devices connect\n")
	} else {
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "\tNAME\tID\tOS\tSTATUS\tLAST SEEN\tFOLDERS")
		for _, d := range list.Items {
			mark := ""
			if d.ID == here {
				mark = "*"
			}
			var folders []string
			for _, f := range d.Folders {
				access := "ro"
				if f.Write {
					access = "rw"
				}
				folders = append(folders, f.Path+" ("+access+")")
			}
			seen := "-"
			if !d.LastSeen.IsZero() {
				seen = humanAge(d.LastSeen) + " ago"
			}
			system := strings.Trim(d.OS+"/"+d.Arch, "/")
			fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", mark, d.Name, d.ID, defaultStr(system, "-"),
				defaultStr(string(d.Status), "-"), seen, defaultStr(strings.Join(folders, ", "), "-"))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if here != "" {
			b.WriteString("* this computer\n")
		}
	}
	_, err := out.Write(b.Bytes())
	return err
}

func newDevicesRenameCmd() *cobra.Command {
	var platformURL string
	cmd := &cobra.Command{
		Use:   "rename NAME [ID]",
		Args:  cobra.RangeArgs(1, 2),
		Short: "Rename this device, or another of yours by its id.",
		Example: `  latere devices rename "Work laptop"
  latere devices rename "Old desktop" dev-abcdefghijklmnop`,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, policy, err := thisDevice()
			if err != nil {
				return err
			}
			name := device.CleanName(args[0])
			id := policy.Device
			if len(args) == 2 {
				id = args[1]
			}
			if id == "" {
				return device.Fail(device.CodeNotConnected, "name a device id from `latere devices list`", nil)
			}
			if err = (deviceAuth{platformURL: platformURL}).rename(cmd.Context(), id, name); err != nil {
				return wrapReposErr(err)
			}
			if id == policy.Device {
				policy.Name = name
				if err = device.SavePolicy(paths.PolicyFile(), policy); err != nil {
					return device.Fail(device.CodePolicyInvalid, "write the new name into the host policy file", err)
				}
			}
			fprintf(cmd.ErrOrStderr(), "Renamed %s to %s.\n", id, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	return cmd
}

func newDevicesRemoveCmd() *cobra.Command {
	var platformURL string
	cmd := &cobra.Command{
		Use:   "remove [ID]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Remove this device, or another of yours by its id.",
		Long: `Remove a device: sessions can no longer use it, every command a session
started on it stops, and its key stops working. On this computer it also
deletes the key and the host policy file; the activity log stays.

Removing is final. Connecting the computer again makes a new device.`,
		Example: `  latere devices remove
  latere devices remove dev-abcdefghijklmnop`,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, policy, err := thisDevice()
			if err != nil {
				return err
			}
			id := policy.Device
			if len(args) == 1 {
				id = args[0]
			}
			if id == "" {
				return device.Fail(device.CodeNotConnected, "name a device id from `latere devices list`", nil)
			}
			if err = (deviceAuth{platformURL: platformURL}).remove(cmd.Context(), id); err != nil {
				return device.Fail(device.CodeRemoveFailed, "", wrapReposErr(err))
			}
			if id == policy.Device {
				if err = device.Forget(paths.Keys(), paths.PolicyFile(), id, true); err != nil {
					return device.Fail(device.CodeKeyUnavailable, "the device was removed, and on this computer: "+err.Error(), err)
				}
			}
			fprintf(cmd.ErrOrStderr(), "Removed %s.\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	return cmd
}

// newDevicesControlCmd is a command that asks the engine serving this
// computer for one action through its control socket, and prints said once
// it is done.
func newDevicesControlCmd(use, route, short, long, said string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Args:    cobra.NoArgs,
		Short:   short,
		Long:    long,
		Example: "  latere devices " + use,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := device.DefaultPaths()
			if err != nil {
				return device.Fail(device.CodeServeFailed, "resolve the state directory", err)
			}
			if _, err = device.ControlPost(cmd.Context(), paths.Socket(), route); err != nil {
				return err
			}
			fprintln(cmd.ErrOrStderr(), said)
			return nil
		},
	}
}

func newDevicesPolicyCmd() *cobra.Command {
	var (
		forget       []string
		sessionStart string
		network      networkFlags
		yes          bool
	)
	cmd := &cobra.Command{
		Use:   "policy",
		Args:  cobra.NoArgs,
		Short: "Print the host policy file, check it, and change how sessions start, the network and remembered triggers.",
		Long: `Print the path and the content of the host policy file,
~/.config/latere/device.yaml, and check it: the folders and read paths
against the rules of the sandbox, the network, and each remembered
trigger.

--session-start allow lets sessions you start, and your triggers that run
on a schedule, start on this computer without asking; a trigger that runs
on mail or a webhook still asks. --session-start ask makes every session
ask. Either applies to the next session, even while this computer is
serving. Each command that changes something is still approved in the
chat, unless you approved a matching one before.

--network sets what commands may reach: allowlist, the hosts --host names
(by default the git and package hosts); none, no host; or any, any public
host but this computer, the networks it is on, and the hosts --block
names. With any, everything readable in a granted folder can be sent to
any public host, so it shows what any allows and asks you to type yes, as
connect does; --yes answers for you. Any is enforced by macOS's own
sandbox alone, and refused elsewhere. Under any, --host names a host on a
port beyond 443 and 80 as HOST:PORT. A --block entry is a host, an address
or *.example.com for the names below example.com, alone or as HOST:PORT.
--host and --block each name the whole list. The network changes at the
next 'latere devices connect'; 'latere devices allow' reaches a running
session at once.

A remembered trigger runs on this computer with no one asked, until its
agent changes or its date passes. --forget TRIGGER removes one, and the
trigger asks again at its next run, even while this computer is serving.`,
		Example: `  latere devices policy
  latere devices policy --session-start ask
  latere devices policy --network any --block internal.example.com --block "*.example.com"
  latere devices policy --network allowlist --host github.com --host proxy.golang.org
  latere devices policy --forget trg_01jaq7v3k9m2wxyz`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := device.DefaultPaths()
			if err != nil {
				return device.Fail(device.CodeServeFailed, "resolve the configuration directory", err)
			}
			path := paths.PolicyFile()
			policy, ok, err := loadPolicy(path)
			if err != nil {
				return err
			}
			if !ok {
				return device.Fail(device.CodeNotConnected, "there is no host policy file at "+path, nil)
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			flags := cmd.Flags()
			if flags.Changed("session-start") && sessionStart != device.SessionStartAllow && sessionStart != device.SessionStartAsk {
				return device.Fail(device.CodePolicyInvalid, fmt.Sprintf("--session-start is %q; it is allow or ask", sessionStart), nil)
			}
			network.modeSet, network.hostsSet, network.blockedSet = flags.Changed("network"), flags.Changed("host"), flags.Changed("block")
			network.hosts, network.blocked = nonEmpty(network.hosts), nonEmpty(network.blocked)
			if network.changed() {
				if err = changeNetwork(cmd, paths, &policy, network, yes); err != nil {
					return err
				}
			}
			if flags.Changed("session-start") {
				policy.SessionStart = sessionStart
				if err = device.SavePolicy(path, policy); err != nil {
					return device.Fail(device.CodePolicyInvalid, "write the host policy file", err)
				}
				fprintf(errOut, "session_start is %s from the next session on.\n", sessionStart)
			}
			if len(forget) > 0 {
				for _, trigger := range forget {
					if !policy.Forget(trigger) {
						return device.Fail(device.CodePolicyInvalid, "no remembered trigger is named "+trigger, nil)
					}
				}
				if err = device.SavePolicy(path, policy); err != nil {
					return device.Fail(device.CodePolicyInvalid, "write the host policy file", err)
				}
				fprintf(errOut, "Forgot %s; it asks again at its next run.\n", strings.Join(forget, ", "))
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return device.Fail(device.CodePolicyInvalid, "", err)
			}
			fprintf(out, "%s\n\n%s\n\n%s\n\n%s", path, strings.TrimRight(string(raw), "\n"), device.SessionStartNotice(policy), networkScreen(policy))
			refusal := policyRefusal(policy, paths, path+": ")
			now := time.Now()
			if len(policy.RememberedTriggers) > 0 {
				fprintln(out, "\nRemembered triggers, each running here with no one asked until its agent changes or its date passes:")
				for _, r := range policy.RememberedTriggers {
					state := "until " + r.Until.Local().Format("2 January 2006")
					if !now.Before(r.Until) {
						state = "expired; it asks again"
					}
					fprintf(out, "  %s  agent version %s  %s\n", r.Trigger, r.AgentVersion, state)
				}
			}
			if refusal != nil {
				return refusal
			}
			fprintln(out, "\nThe host policy file is valid.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&forget, "forget", nil, "forget a remembered trigger by its id (repeatable)")
	f.StringVar(&sessionStart, "session-start", "", "whether your own sessions and scheduled triggers start without asking here: allow or ask")
	f.StringVar(&network.mode, "network", "", "what commands may reach: allowlist, none, or any public host but those --block names")
	f.StringArrayVar(&network.hosts, "host", nil, "a host on the allowlist, or under any a HOST:PORT on a port beyond 443 and 80 (repeatable; names the whole list)")
	f.StringArrayVar(&network.blocked, "block", nil, "a host the network any refuses: a host, an address or *.example.com, alone or as HOST:PORT (repeatable; names the whole list)")
	f.BoolVar(&yes, "yes", false, "set the network any without asking to type yes")
	return cmd
}

func newDevicesActivityCmd() *cobra.Command {
	var (
		followF  bool
		since    string
		deviceID string
		jsonF    bool
	)
	cmd := &cobra.Command{
		Use:   "activity",
		Args:  cobra.NoArgs,
		Short: "Print the activity log of this computer, and follow it with -f.",
		Long: `Print what sessions asked this computer to do: every command, every file
read or written, and every approval, with its time and session. The log
holds no file content and no output. It is this computer's own account,
kept whatever the platform records.

--since takes a duration such as 1h or a time such as 2026-10-18T09:00:00Z.`,
		Example: `  latere devices activity
  latere devices activity -f
  latere devices activity --since 24h`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, policy, err := thisDevice()
			if err != nil {
				return err
			}
			id := defaultStr(deviceID, policy.Device)
			if id == "" {
				return device.Fail(device.CodeNotConnected, "name the device whose log to read with --device", nil)
			}
			from, err := device.ParseSince(since, time.Now())
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			out := cmd.OutOrStdout()
			show := func(e device.Entry) error {
				if jsonF {
					return json.NewEncoder(out).Encode(e)
				}
				_, err := fmt.Fprintln(out, e.Line(paths.Home))
				return err
			}
			dir := paths.ActivityDir(id)
			if followF {
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				return device.FollowActivity(ctx, dir, from, show)
			}
			entries, err := device.ReadActivity(dir, from)
			if err != nil {
				return device.Fail(device.CodeServeFailed, "read the activity log", err)
			}
			for _, e := range entries {
				if err = show(e); err != nil {
					return err
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&followF, "follow", "f", false, "keep printing lines as they are written")
	f.StringVar(&since, "since", "", "print from this time or this long ago")
	f.StringVar(&deviceID, "device", "", "read the log of another device this computer was, by its id")
	f.BoolVar(&jsonF, "json", false, "print each line as JSON")
	return cmd
}
