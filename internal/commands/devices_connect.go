// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	goruntime "runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	cellaclient "latere.ai/x/cella/client"
	"latere.ai/x/cella/device"
	"latere.ai/x/cella/runtime"
	"latere.ai/x/cella/runtime/local"
	"latere.ai/x/sdk-go/account"
	"latere.ai/x/sdk-go/latere"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/deviceterm"
	"github.com/latere-ai/latere-cli/internal/upgrade"
)

// What a test replaces: the effective user, whether stdin is a person's
// terminal, the driver the engine runs, and the lookup of the newest
// release.
var (
	deviceEUID        = os.Geteuid
	deviceInteractive = func(in io.Reader) bool {
		f, ok := in.(*os.File)
		return ok && term.IsTerminal(int(f.Fd()))
	}
	deviceDriver  = func(o local.Options) (runtime.Driver, error) { return local.New(o) }
	latestRelease = upgrade.ResolveLatest
)

func newDevicesConnectCmd(version string) *cobra.Command {
	var (
		name, network, sessionStart, apiURL, platformURL string
		folders, hosts                                   []string
	)
	cmd := &cobra.Command{
		Use:   "connect",
		Args:  cobra.NoArgs,
		Short: "Connect this computer as one of your devices and serve it until Ctrl-C.",
		Long: `Connect this computer so the sessions you start in Latere can work on it.

The first time, it shows what sessions may use (the folders, the network)
and asks you to type yes, writes the host policy file
~/.config/latere/device.yaml, and connects the computer as one of your
devices. After that it serves the device this computer already is.

It runs in the foreground until Ctrl-C. Sessions you start, and your
triggers that run on a schedule, start here without asking, and each
prints a line when it starts; a trigger that runs on mail or a webhook
asks for your approval here first. With --session-start ask, every
session asks. Each command that changes something is still approved in
the chat, unless you approved a matching one before. Every command and
file a session touches is printed and written to the activity log.
Ctrl-C stops every command a session started on this computer, and
'latere devices pause' stops them while it keeps running.

A folder is shared read-write unless it ends in :ro. With no --folder,
the current directory is shared. The network is an allowlist of git and
package hosts unless --host names the hosts, or --network none allows
none. To change any of it later, edit the host policy file and connect
again; 'latere devices policy --session-start' changes how sessions start
at once.`,
		Example: `  latere devices connect
  latere devices connect --folder ~/code/site --folder ~/notes:ro
  latere devices connect --network none
  latere devices connect --session-start ask
  latere devices connect --name "Work laptop" --host github.com --host proxy.golang.org`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			paths, lock, err := deviceStart()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Release() }()
			policy, ok, err := device.LoadPolicy(paths.PolicyFile())
			if err != nil {
				return device.Fail(device.CodePolicyInvalid, "", err)
			}
			flagged := cmd.Flags().Changed("name") || cmd.Flags().Changed("folder") ||
				cmd.Flags().Changed("network") || cmd.Flags().Changed("host") || cmd.Flags().Changed("session-start")
			if ok && policy.Device != "" && flagged {
				return device.Fail(device.CodeAlreadyConnected, paths.PolicyFile()+" names the device "+policy.Device, nil)
			}
			if !ok || policy.Device == "" {
				request := connectRequest{
					name: name, folders: folders, network: network, hosts: hosts, sessionStart: sessionStart,
					networkSet: cmd.Flags().Changed("network"), hostsSet: cmd.Flags().Changed("host"),
					sessionStartSet: cmd.Flags().Changed("session-start"),
				}
				if _, err = enrollDevice(ctx, cmd, paths, policy, request, deviceAuth{apiURL: apiURL, platformURL: platformURL}); err != nil {
					return err
				}
			}
			return serveDevice(ctx, cmd, paths, deviceAuth{apiURL: apiURL, platformURL: platformURL}, version, true)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "the name this computer has among your devices (default: the computer's own name)")
	f.StringArrayVar(&folders, "folder", nil, "a folder sessions may use, PATH or PATH:ro (repeatable; default: the current directory)")
	f.StringVar(&network, "network", device.NetworkAllowlist, "what sessions may reach: allowlist or none")
	f.StringArrayVar(&hosts, "host", nil, "a host on the allowlist (repeatable; default: git and package hosts)")
	f.StringVar(&sessionStart, "session-start", device.SessionStartAllow, "whether your own sessions and scheduled triggers start without asking here: allow or ask")
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&platformURL, "platform-url", "", platformURLUsage)
	return cmd
}

// The two places `serve` takes the person's tokens from.
const (
	credentialsLogin  = "login"
	credentialsClient = "client"
)

func newDevicesServeCmd(version string) *cobra.Command {
	var apiURL, platformURL, credentials string
	cmd := &cobra.Command{
		Use:   "serve",
		Args:  cobra.NoArgs,
		Short: "Serve this computer with no terminal, for a desktop application or a background service.",
		Long: `Serve this computer with no one to answer in the terminal. Under
session_start allow, sessions you start and your scheduled triggers start
without asking. A desktop application or a second terminal answers every
other session's request through the control socket,
$XDG_STATE_HOME/latere/devices/control.sock, and is told of each session
that starts; with none attached, a session that asks is refused unless it
is a scheduled trigger you remembered.

On a computer that is not connected yet, serve waits with its control
socket open, and a client connects it there with the consent it showed.
Once the device is removed, serve waits again. A client also changes the
host policy and removes the device through the socket.

With --credentials client, serve never reads your saved sign-in: the
client hands it short-lived tokens for Cella and the platform through the
socket, and the status says when it needs new ones. The status and every
operation are printed as they happen, as connect prints them.`,
		Example: `  latere devices serve
  latere devices serve --credentials client`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			auth := deviceAuth{apiURL: apiURL, platformURL: platformURL}
			switch credentials {
			case credentialsLogin:
			case credentialsClient:
				auth.held = device.NewCredentials(device.CredentialsOptions{PlatformAudience: latere.DefaultAudience, Read: readDeviceToken})
			default:
				return fmt.Errorf("--credentials is %q; it is login or client", credentials)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			paths, lock, err := deviceStart()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Release() }()
			return serveDevice(ctx, cmd, paths, auth, version, false)
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&platformURL, "platform-url", "", platformURLUsage)
	f.StringVar(&credentials, "credentials", credentialsLogin, "where the person's tokens come from: login, the saved sign-in, or client, handed through the control socket")
	return cmd
}

// deviceStart refuses what cannot be safe, root and a build that cannot
// verify its own upgrades, and takes the lock that keeps one engine per
// computer, before anything reaches the platform.
func deviceStart() (device.Paths, *device.Lock, error) {
	if deviceEUID() == 0 {
		return device.Paths{}, nil, device.Fail(device.CodeRunsAsRoot, "the effective user id is 0", nil)
	}
	if !upgrade.HasReleaseKey() {
		return device.Paths{}, nil, cliRefusal(codeUnverifiedBuild, "this build holds no release key to verify an upgrade with", nil)
	}
	paths, err := device.DefaultPaths()
	if err != nil {
		return device.Paths{}, nil, device.Fail(device.CodeServeFailed, "resolve the configuration and state directories", err)
	}
	lock, err := device.TakeLock(paths.LockFile())
	if err != nil {
		return device.Paths{}, nil, err
	}
	return paths, lock, nil
}

// connectRequest is what connect was asked for on its command line.
type connectRequest struct {
	name, network, sessionStart           string
	folders, hosts                        []string
	networkSet, hostsSet, sessionStartSet bool
}

// query is the request as the engine reads a connect's terms: a flag left
// unset is empty, which reads as what the previous file said, then the
// default.
func (r connectRequest) query() device.ConnectQuery {
	q := device.ConnectQuery{Name: r.name, Folders: r.folders}
	if r.networkSet {
		q.Network = r.network
	}
	if r.hostsSet {
		q.Hosts = r.hosts
	}
	if r.sessionStartSet {
		q.SessionStart = r.sessionStart
	}
	return q
}

// proposal is the host policy connect proposes: the flags, then what an
// earlier file said, then the defaults, with the folders resolved against
// the working directory.
func (r connectRequest) proposal(previous device.Policy, home string) (device.Policy, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return device.Policy{}, err
	}
	return device.ProposePolicy(previous, r.query(), home, cwd, device.DefaultComputerName)
}

// enrollDevice connects this computer from the terminal: the screen and its
// typed yes, then connectAs.
func enrollDevice(ctx context.Context, cmd *cobra.Command, paths device.Paths, previous device.Policy, r connectRequest, auth deviceAuth) (device.Policy, error) {
	errOut := cmd.ErrOrStderr()
	org, err := auth.orgContext(ctx)
	if err != nil {
		return device.Policy{}, err
	}
	if org != "" {
		return device.Policy{}, device.Fail(device.CodeOrgContext, "the saved login is in the context of the organization "+org, nil)
	}
	p, err := r.proposal(previous, paths.Home)
	if err != nil {
		return device.Policy{}, device.Fail(device.CodePolicyInvalid, "", err)
	}
	if problems := p.Problems(paths.Home); len(problems) > 0 {
		return device.Policy{}, device.Fail(device.CodePolicyInvalid, strings.Join(problems, "; "), nil)
	}
	if err = device.CheckDriver(p, paths, goruntime.GOOS, deviceDriver); err != nil {
		return device.Policy{}, device.Fail(device.CodePolicyInvalid, "", err)
	}
	fprint(errOut, device.ConnectScreen(p))
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return device.Policy{}, device.Fail(device.CodeNotConfirmed, "read the answer", err)
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		fprintln(errOut)
		return device.Policy{}, device.Fail(device.CodeNotConfirmed, fmt.Sprintf("the answer was %q", strings.TrimSpace(answer)), nil)
	}
	return connectAs(ctx, paths, p, auth, errOut)
}

// connectAs is what connecting does once the person consented, in the
// terminal: the engine's Enrollment as the person, then what it did
// printed.
func connectAs(ctx context.Context, paths device.Paths, p device.Policy, auth deviceAuth, errOut io.Writer) (device.Policy, error) {
	client, err := auth.cella()
	if err != nil {
		return device.Policy{}, device.Fail(device.CodeRefused, "", err)
	}
	done, err := newEnrollment(paths, client, auth).Connect(ctx, p)
	if err != nil {
		return device.Policy{}, err
	}
	// The saved login connected it, so its person owns the device: an
	// application that serves it later refuses another person's tokens.
	if err := device.RecordOwner(paths, done.Policy.Device, savedLoginSubject()); err != nil {
		fprintf(errOut, "  warning: whose device this computer is could not be recorded: %v\n", err)
	}
	if done.NameError != nil {
		fprintf(errOut, "Note: this computer is connected, and its name was not set (%v). Set it with `latere devices rename %q`.\n",
			wrapReposErr(done.NameError), p.Name)
	}
	fprintf(errOut, "Connected this computer as %s (%s).\n", p.Name, done.Policy.Device)
	return done.Policy, nil
}

// newEnrollment is the engine's Enrollment as the person: the environment
// created through client, the platform's part through auth, and the device
// kind label beside the tenant labels.
func newEnrollment(paths device.Paths, client *cellaclient.Client, auth deviceAuth) *device.Enrollment {
	return device.NewEnrollment(device.EnrollmentOptions{
		Paths: paths, Cella: client, Platform: laterePlatform{auth: auth},
		Labels: map[string]string{account.LabelKind: account.KindDevice},
	})
}

// savedLoginSubject is the person the saved login names, "" when there is
// none or it does not read.
func savedLoginSubject() string {
	tok, err := api.LoadAuthToken()
	if err != nil {
		return ""
	}
	info, err := principalFromJWT(tok.AccessToken)
	if err != nil {
		return ""
	}
	return info.Sub
}

// serveDevice runs the engine with its control socket, and with the
// terminal as a client when attended. Unattended, it is `serve`, which waits
// to be connected through the socket.
func serveDevice(ctx context.Context, cmd *cobra.Command, paths device.Paths, auth deviceAuth, version string, attended bool) error {
	base := resolveCellaURL(auth.apiURL)
	loginSubject := ""
	if auth.held == nil {
		loginSubject = savedLoginSubject()
	}
	// One client serves the whole run: its token source reads the saved
	// login, or the client's held token, on each call.
	client, err := auth.cella()
	if err != nil {
		return err
	}
	engine := device.New(device.Config{
		Paths: paths, URL: base, Keys: paths.Keys(),
		Account: device.NewCellaAccount(client), Version: version, NewDriver: deviceDriver,
		Labels:      toposLabels,
		Upgrade:     serveUpgrade(version),
		Enroll:      deviceEnroller{Enrollment: newEnrollment(paths, client, auth), errOut: cmd.ErrOrStderr()},
		Credentials: auth.held, LoginSubject: loginSubject, WaitToConnect: !attended,
	})
	ln, err := device.ListenControl(paths.Socket())
	if err != nil {
		return err
	}
	serving, stopServing := context.WithCancel(ctx)
	defer stopServing()
	controlled := make(chan error, 1)
	go func() { controlled <- engine.ServeControl(serving, ln) }()
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		deviceterm.Terminal{
			Engine: engine, In: cmd.InOrStdin(), Out: cmd.ErrOrStderr(),
			Interactive: attended && deviceInteractive(cmd.InOrStdin()), Home: paths.Home,
		}.Run(serving)
	}()
	runErr := engine.Run(ctx)
	stopServing()
	<-printed
	if err := <-controlled; err != nil && runErr == nil {
		runErr = device.Fail(device.CodeControlUnavailable, "", err)
	}
	if runErr == nil {
		fprintln(cmd.ErrOrStderr(), "Stopped serving this computer; every command a session started here was stopped.")
	}
	return runErr
}

// serveUpgrade is the engine's daily question for a newer release, or nil
// where release awareness is turned off (LATERE_NO_UPDATE_CHECK, CI), as
// for a copy a desktop application bundles and updates itself.
func serveUpgrade(version string) func(context.Context) (string, bool) {
	if upgrade.Disabled() {
		return nil
	}
	return func(ctx context.Context) (string, bool) {
		latest, err := latestRelease(ctx, nil)
		if err != nil {
			return "", false
		}
		return latest, upgrade.Newer(version, latest)
	}
}

// deviceEnroller is the engine's Enroller for what a client does through
// the control socket: the Enrollment, with what a connect did printed for
// serve's own output.
type deviceEnroller struct {
	*device.Enrollment
	errOut io.Writer
}

// Connect connects this computer as the person, refusing an organization's
// context as connect does. What it did is printed for serve's own output,
// and a name the platform refused is answered to the client too.
func (d deviceEnroller) Connect(ctx context.Context, p device.Policy) (device.Connected, error) {
	done, err := d.Enrollment.Connect(ctx, p)
	if err != nil {
		return device.Connected{}, err
	}
	if done.NameError != nil {
		done.NameError = wrapReposErr(done.NameError)
		fprintf(d.errOut, "Note: this computer is connected, and its name was not set (%v).\n", done.NameError)
	}
	fprintf(d.errOut, "Connected this computer as %s (%s).\n", p.Name, done.Policy.Device)
	return done, nil
}
