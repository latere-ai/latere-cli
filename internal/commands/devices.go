// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	goruntime "runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/devices"
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
func thisDevice() (devices.Paths, devices.Policy, error) {
	paths, err := devices.DefaultPaths()
	if err != nil {
		return devices.Paths{}, devices.Policy{}, devices.Fail(devices.CodeServeFailed, "resolve the configuration directory", err)
	}
	policy, _, err := devices.LoadPolicy(paths.PolicyFile())
	if err != nil {
		return paths, devices.Policy{}, devices.Fail(devices.CodePolicyInvalid, "", err)
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
			var list deviceList
			if err = platformCall(cmd.Context(), platformURL, "", http.MethodGet, devicesPath, nil, &list); err != nil {
				return wrapReposErr(err)
			}
			if jsonF {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(list)
			}
			return printDevices(cmd.OutOrStdout(), list, policy.Device)
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// printDevices writes the list as a table.
func printDevices(out io.Writer, list deviceList, here string) error {
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
			if d.LastSeen != nil {
				seen = humanAge(*d.LastSeen) + " ago"
			}
			system := strings.Trim(d.OS+"/"+d.Arch, "/")
			fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", mark, d.Name, d.ID, defaultStr(system, "-"),
				defaultStr(d.Status, "-"), seen, defaultStr(strings.Join(folders, ", "), "-"))
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
			name := devices.CleanName(args[0])
			id := policy.Device
			if len(args) == 2 {
				id = args[1]
			}
			if id == "" {
				return devices.Fail(devices.CodeNotConnected, "name a device id from `latere devices list`", nil)
			}
			if err = platformCall(cmd.Context(), platformURL, "", http.MethodPatch, devicesPath+"/"+id, map[string]string{"name": name}, nil); err != nil {
				return wrapReposErr(err)
			}
			if id == policy.Device {
				policy.Name = name
				if err = devices.SavePolicy(paths.PolicyFile(), policy); err != nil {
					return devices.Fail(devices.CodePolicyInvalid, "write the new name into the host policy file", err)
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
				return devices.Fail(devices.CodeNotConnected, "name a device id from `latere devices list`", nil)
			}
			if err = (deviceAuth{platformURL: platformURL}).remove(cmd.Context(), id); err != nil {
				return devices.Fail(devices.CodeRemoveFailed, "", wrapReposErr(err))
			}
			if id == policy.Device {
				if err = devices.Forget(paths.Keys(), paths.PolicyFile(), id, true); err != nil {
					return devices.Fail(devices.CodeKeyUnavailable, "the device was removed, and on this computer: "+err.Error(), err)
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
			paths, err := devices.DefaultPaths()
			if err != nil {
				return devices.Fail(devices.CodeServeFailed, "resolve the state directory", err)
			}
			if _, err = devices.ControlPost(cmd.Context(), paths.Socket(), route); err != nil {
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
	)
	cmd := &cobra.Command{
		Use:   "policy",
		Args:  cobra.NoArgs,
		Short: "Print the host policy file, check it, set how sessions start, and forget remembered triggers.",
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

A remembered trigger runs on this computer with no one asked, until its
agent changes or its date passes. --forget TRIGGER removes one, and the
trigger asks again at its next run, even while this computer is serving.`,
		Example: `  latere devices policy
  latere devices policy --session-start ask
  latere devices policy --forget trg_01jaq7v3k9m2wxyz`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := devices.DefaultPaths()
			if err != nil {
				return devices.Fail(devices.CodeServeFailed, "resolve the configuration directory", err)
			}
			path := paths.PolicyFile()
			policy, ok, err := devices.LoadPolicy(path)
			if err != nil {
				return devices.Fail(devices.CodePolicyInvalid, "", err)
			}
			if !ok {
				return devices.Fail(devices.CodeNotConnected, "there is no host policy file at "+path, nil)
			}
			out := cmd.OutOrStdout()
			if cmd.Flags().Changed("session-start") {
				if sessionStart != devices.SessionStartAllow && sessionStart != devices.SessionStartAsk {
					return devices.Fail(devices.CodePolicyInvalid, fmt.Sprintf("--session-start is %q; it is allow or ask", sessionStart), nil)
				}
				policy.SessionStart = sessionStart
				if err = devices.SavePolicy(path, policy); err != nil {
					return devices.Fail(devices.CodePolicyInvalid, "write the host policy file", err)
				}
				fprintf(cmd.ErrOrStderr(), "session_start is %s from the next session on.\n", sessionStart)
			}
			if len(forget) > 0 {
				for _, trigger := range forget {
					if !policy.Forget(trigger) {
						return devices.Fail(devices.CodePolicyInvalid, "no remembered trigger is named "+trigger, nil)
					}
				}
				if err = devices.SavePolicy(path, policy); err != nil {
					return devices.Fail(devices.CodePolicyInvalid, "write the host policy file", err)
				}
				fprintf(cmd.ErrOrStderr(), "Forgot %s; it asks again at its next run.\n", strings.Join(forget, ", "))
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return devices.Fail(devices.CodePolicyInvalid, "", err)
			}
			fprintf(out, "%s\n\n%s\n\n%s\n", path, strings.TrimRight(string(raw), "\n"), devices.SessionStartNotice(policy))
			problems := policy.Problems(paths.Home)
			if len(problems) == 0 {
				if err := devices.CheckDriver(policy, paths, goruntime.GOOS, deviceDriver); err != nil {
					problems = append(problems, err.Error())
				}
			}
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
			if len(problems) > 0 {
				return devices.Fail(devices.CodePolicyInvalid, path+": "+strings.Join(problems, "; "), nil)
			}
			fprintln(out, "\nThe host policy file is valid.")
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&forget, "forget", nil, "forget a remembered trigger by its id (repeatable)")
	cmd.Flags().StringVar(&sessionStart, "session-start", "", "whether your own sessions and scheduled triggers start without asking here: allow or ask")
	return cmd
}

func newDevicesActivityCmd() *cobra.Command {
	var (
		followF bool
		since   string
		device  string
		jsonF   bool
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
			id := defaultStr(device, policy.Device)
			if id == "" {
				return devices.Fail(devices.CodeNotConnected, "name the device whose log to read with --device", nil)
			}
			from, err := devices.ParseSince(since, time.Now())
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			out := cmd.OutOrStdout()
			show := func(e devices.Entry) error {
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
				return devices.FollowActivity(ctx, dir, from, show)
			}
			entries, err := devices.ReadActivity(dir, from)
			if err != nil {
				return devices.Fail(devices.CodeServeFailed, "read the activity log", err)
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
	f.StringVar(&device, "device", "", "read the log of another device this computer was, by its id")
	f.BoolVar(&jsonF, "json", false, "print each line as JSON")
	return cmd
}
