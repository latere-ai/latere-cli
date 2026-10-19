// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	cellaclient "latere.ai/x/cella/client"
	v1 "latere.ai/x/cella/manifest/v1"
	"latere.ai/x/cella/runtime"
	"latere.ai/x/cella/runtime/local"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/devices"
	"github.com/latere-ai/latere-cli/internal/upgrade"
)

// The device kind label every device environment carries beside its
// owner's tenant labels (platform spec 220).
const (
	deviceKindLabel = "platform.latere.ai/kind"
	deviceKindValue = "device"
)

// maxDeviceName is the longest name the platform keeps for a device.
const maxDeviceName = 64

// deviceSandboxes is how many sandboxes a device environment holds at once,
// one per session working on the computer. Cella requires an environment to
// declare a capacity, and placement admits against it.
const deviceSandboxes = 8

// What a test replaces: the effective user, whether stdin is a person's
// terminal, the computer's own name, the driver the engine runs, and the
// lookup of the newest release.
var (
	deviceEUID        = os.Geteuid
	deviceInteractive = func(in io.Reader) bool {
		f, ok := in.(*os.File)
		return ok && term.IsTerminal(int(f.Fd()))
	}
	computerName  = defaultComputerName
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
			policy, ok, err := devices.LoadPolicy(paths.PolicyFile())
			if err != nil {
				return devices.Fail(devices.CodePolicyInvalid, "", err)
			}
			flagged := cmd.Flags().Changed("name") || cmd.Flags().Changed("folder") ||
				cmd.Flags().Changed("network") || cmd.Flags().Changed("host") || cmd.Flags().Changed("session-start")
			if ok && policy.Device != "" && flagged {
				return devices.Fail(devices.CodeAlreadyConnected, paths.PolicyFile()+" names the device "+policy.Device, nil)
			}
			if !ok || policy.Device == "" {
				request := connectRequest{
					name: name, folders: folders, network: network, hosts: hosts, sessionStart: sessionStart,
					networkSet: cmd.Flags().Changed("network"), hostsSet: cmd.Flags().Changed("host"),
					sessionStartSet: cmd.Flags().Changed("session-start"),
				}
				if _, err = enrollDevice(ctx, cmd, paths, policy, request, apiURL, platformURL); err != nil {
					return err
				}
			}
			return serveDevice(ctx, cmd, paths, apiURL, version, true)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "the name this computer has among your devices (default: the computer's own name)")
	f.StringArrayVar(&folders, "folder", nil, "a folder sessions may use, PATH or PATH:ro (repeatable; default: the current directory)")
	f.StringVar(&network, "network", devices.NetworkAllowlist, "what sessions may reach: allowlist or none")
	f.StringArrayVar(&hosts, "host", nil, "a host on the allowlist (repeatable; default: git and package hosts)")
	f.StringVar(&sessionStart, "session-start", devices.SessionStartAllow, "whether your own sessions and scheduled triggers start without asking here: allow or ask")
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&platformURL, "platform-url", "", platformURLUsage)
	return cmd
}

func newDevicesServeCmd(version string) *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "serve",
		Args:  cobra.NoArgs,
		Short: "Serve this computer with no terminal, for a desktop application or a background service.",
		Long: `Serve the device this computer already is, with no one to answer in the
terminal. Under session_start allow, sessions you start and your
scheduled triggers start without asking. A desktop application or a
second terminal answers every other session's request through the control
socket, $XDG_STATE_HOME/latere/devices/control.sock, and is told of each
session that starts; with none attached, a session that asks is refused
unless it is a scheduled trigger you remembered.

Connect the computer first with 'latere devices connect'. The status and
every operation are printed as they happen, as connect prints them.`,
		Example: `  latere devices serve`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			paths, lock, err := deviceStart()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Release() }()
			return serveDevice(ctx, cmd, paths, apiURL, version, false)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// deviceStart refuses what cannot be safe, root and a build that cannot
// verify its own upgrades, and takes the lock that keeps one engine per
// computer, before anything reaches the platform.
func deviceStart() (devices.Paths, *devices.Lock, error) {
	if deviceEUID() == 0 {
		return devices.Paths{}, nil, devices.Fail(devices.CodeRunsAsRoot, "the effective user id is 0", nil)
	}
	if !upgrade.HasReleaseKey() {
		return devices.Paths{}, nil, devices.Fail(devices.CodeUnverifiedBuild, "this build holds no release key to verify an upgrade with", nil)
	}
	paths, err := devices.DefaultPaths()
	if err != nil {
		return devices.Paths{}, nil, devices.Fail(devices.CodeServeFailed, "resolve the configuration and state directories", err)
	}
	lock, err := devices.TakeLock(paths.LockFile())
	if err != nil {
		return devices.Paths{}, nil, err
	}
	return paths, lock, nil
}

// connectRequest is what connect was asked for on its command line.
type connectRequest struct {
	name, network, sessionStart           string
	folders, hosts                        []string
	networkSet, hostsSet, sessionStartSet bool
}

// proposal is the host policy connect proposes: the flags, then what an
// earlier file said, then the defaults.
func (r connectRequest) proposal(previous devices.Policy, home string) (devices.Policy, error) {
	p := devices.Policy{
		Name: previous.Name, SessionStart: previous.SessionStart, Folders: previous.Folders, Network: previous.Network,
		ReadPaths: previous.ReadPaths, RememberedTriggers: previous.RememberedTriggers,
	}
	if r.sessionStartSet || p.SessionStart == "" {
		p.SessionStart = r.sessionStart
	}
	if r.name != "" {
		p.Name = r.name
	}
	if p.Name == "" {
		p.Name = computerName()
	}
	p.Name = cleanDeviceName(p.Name)
	if len(r.folders) > 0 || len(p.Folders) == 0 {
		values := r.folders
		if len(values) == 0 {
			values = []string{"."}
		}
		cwd, err := os.Getwd()
		if err != nil {
			return devices.Policy{}, err
		}
		p.Folders = nil
		for _, v := range values {
			folder, err := devices.ParseFolder(v, cwd, home)
			if err != nil {
				return devices.Policy{}, err
			}
			folder.Path = devices.Shorten(home, folder.Path)
			p.Folders = append(p.Folders, folder)
		}
	}
	if r.networkSet || p.Network.Mode == "" {
		p.Network.Mode = r.network
	}
	switch {
	case p.Network.Mode == devices.NetworkNone:
		p.Network.Hosts = nil
	case r.hostsSet:
		p.Network.Hosts = r.hosts
	case len(p.Network.Hosts) == 0:
		p.Network.Hosts = devices.DefaultHosts
	}
	return p, nil
}

// enrollDevice connects this computer: the screen and its typed yes, the
// host policy file, the environment at Cella created as the person, and its
// name at the platform. The key is minted when the engine starts.
func enrollDevice(ctx context.Context, cmd *cobra.Command, paths devices.Paths, previous devices.Policy, r connectRequest, apiURL, platformURL string) (devices.Policy, error) {
	errOut := cmd.ErrOrStderr()
	tok, err := api.LoadAuthToken()
	if err != nil {
		return devices.Policy{}, devices.Fail(devices.CodeNoSignIn, "", err)
	}
	info, err := principalFromJWT(tok.AccessToken)
	if err != nil {
		return devices.Policy{}, devices.Fail(devices.CodeNoSignIn, "", err)
	}
	if info.OrgID != "" {
		return devices.Policy{}, devices.Fail(devices.CodeOrgContext, "the saved login is in the context of the organization "+info.OrgID, nil)
	}
	p, err := r.proposal(previous, paths.Home)
	if err != nil {
		return devices.Policy{}, devices.Fail(devices.CodePolicyInvalid, "", err)
	}
	if problems := p.Problems(paths.Home); len(problems) > 0 {
		return devices.Policy{}, devices.Fail(devices.CodePolicyInvalid, strings.Join(problems, "; "), nil)
	}
	if err = devices.CheckDriver(p, paths, goruntime.GOOS, deviceDriver); err != nil {
		return devices.Policy{}, devices.Fail(devices.CodePolicyInvalid, "", err)
	}
	fprint(errOut, devices.ConnectScreen(p))
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return devices.Policy{}, devices.Fail(devices.CodeNotConfirmed, "read the answer", err)
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		fprintln(errOut)
		return devices.Policy{}, devices.Fail(devices.CodeNotConfirmed, fmt.Sprintf("the answer was %q", strings.TrimSpace(answer)), nil)
	}
	if err = devices.SavePolicy(paths.PolicyFile(), p); err != nil {
		return devices.Policy{}, devices.Fail(devices.CodePolicyInvalid, "write the host policy file", err)
	}
	labels, err := tenantLabels(ctx, platformURL)
	if err != nil {
		return devices.Policy{}, devices.Fail(devices.CodeRefused, "", err)
	}
	labels[deviceKindLabel] = deviceKindValue
	id := "dev-" + strings.ToLower(rand.Text()[:16])
	body, err := json.Marshal(v1.Environment{
		APIVersion: v1.APIVersion, Kind: v1.KindEnvironment,
		Metadata: v1.Metadata{Name: id, Labels: labels},
		Spec: v1.EnvironmentSpec{
			Mode: v1.EnvironmentWorker, Isolation: v1.IsolationProcess, Workers: v1.WorkersSpec{Max: 1},
			Capacity: v1.Capacity{Sandboxes: deviceSandboxes},
		},
	})
	if err != nil {
		return devices.Policy{}, devices.Fail(devices.CodeServeFailed, "encode the environment", err)
	}
	client, err := cellaClient(apiURL)
	if err != nil {
		return devices.Policy{}, devices.Fail(devices.CodeRefused, "", err)
	}
	if _, _, err = client.CreateEnvironment(ctx, cellaclient.JSON(body)); err != nil {
		return devices.Policy{}, createRefusal(ctx, err, platformURL)
	}
	p.Device = id
	if err = devices.SavePolicy(paths.PolicyFile(), p); err != nil {
		return devices.Policy{}, devices.Fail(devices.CodePolicyInvalid, "write the device into the host policy file", err)
	}
	if err = platformCall(ctx, platformURL, "", http.MethodPatch, "/v1/account/devices/"+id, map[string]string{"name": p.Name}, nil); err != nil {
		fprintf(errOut, "Note: this computer is connected, and its name was not set (%v). Set it with `latere devices rename %q`.\n",
			wrapReposErr(err), p.Name)
	}
	fprintf(errOut, "Connected this computer as %s (%s).\n", p.Name, id)
	return p, nil
}

// createRefusal is the answer to an environment create the platform
// refused. Its reason travels in Cella's developer detail, and each reason
// the platform names gets its own refusal; the device limit is told with the
// limit the platform answers for the person.
func createRefusal(ctx context.Context, err error, platformURL string) error {
	var ce *cellaclient.Error
	if !errors.As(err, &ce) {
		return devices.Fail(devices.CodeRefused, "", err)
	}
	switch reason := platformReason(ce.Detail); reason {
	case devices.CodePersonalOnly, devices.CodeSignInRequired, devices.CodeKeyRefused, devices.CodeInvalid:
		return devices.Fail(reason, ce.Detail, err)
	case devices.CodeLimit:
		refusal := devices.Fail(devices.CodeLimit, ce.Detail, err)
		var list deviceList
		if lerr := platformCall(ctx, platformURL, "", http.MethodGet, devicesPath, nil, &list); lerr == nil && list.Limit > 0 {
			refusal.Hint = fmt.Sprintf("You have %d devices, and one person may connect %d. %s", len(list.Items), list.Limit, refusal.Hint)
		}
		return refusal
	}
	if ce.Status == http.StatusUnauthorized {
		return devices.Fail(devices.CodeSignInRequired, ce.Detail, err)
	}
	return devices.Fail(devices.CodeRefused, defaultStr(ce.Detail, ce.Error()), err)
}

// platformReason is the platform's reason in a Cella refusal's detail, which
// reads "forbidden: environment.create: device_limit": the last word, when
// it is a code.
func platformReason(detail string) string {
	_, word, ok := strings.CutLast(detail, ": ")
	if !ok {
		return ""
	}
	word = strings.TrimSpace(word)
	for _, r := range word {
		if !unicode.IsLower(r) && r != '_' {
			return ""
		}
	}
	return word
}

// serveDevice runs the engine with its control socket, and with the
// terminal as a client when attended.
func serveDevice(ctx context.Context, cmd *cobra.Command, paths devices.Paths, apiURL, version string, attended bool) error {
	base, err := resolveCellaURL(apiURL)
	if err != nil {
		return err
	}
	engine := devices.New(devices.Config{
		Paths: paths, URL: base, Keys: paths.Keys(),
		Account: deviceAccount{apiURL: apiURL}, Version: version, NewDriver: deviceDriver,
		Upgrade: upgradeAvailable(version),
	})
	ln, err := devices.ListenControl(paths.Socket())
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
		devices.Terminal{
			Engine: engine, In: cmd.InOrStdin(), Out: cmd.ErrOrStderr(),
			Interactive: attended && deviceInteractive(cmd.InOrStdin()), Home: paths.Home,
		}.Run(serving)
	}()
	runErr := engine.Run(ctx)
	stopServing()
	<-printed
	if err := <-controlled; err != nil && runErr == nil {
		runErr = devices.Fail(devices.CodeControlUnavailable, "", err)
	}
	if runErr == nil {
		fprintln(cmd.ErrOrStderr(), "Stopped serving this computer; every command a session started here was stopped.")
	}
	return runErr
}

// upgradeAvailable is the engine's daily question for a newer release.
func upgradeAvailable(version string) func(context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) {
		latest, err := latestRelease(ctx, nil)
		if err != nil {
			return "", false
		}
		return latest, upgrade.Newer(version, latest)
	}
}

// deviceAccount is the engine's Account: the person's own Cella token,
// minted from the saved login, as every Environments command presents.
type deviceAccount struct{ apiURL string }

func (a deviceAccount) MintKey(ctx context.Context, env string, ttl time.Duration) (devices.Key, error) {
	client, err := cellaClient(a.apiURL)
	if err != nil {
		return devices.Key{}, err
	}
	key, _, err := client.MintEnvironmentKeyFor(ctx, env, ttl)
	if err != nil {
		return devices.Key{}, removedOr(err)
	}
	return devices.Key{Environment: env, Token: key.Token, JTI: key.JTI, ExpiresAt: key.Expires}, nil
}

func (a deviceAccount) RevokeKey(ctx context.Context, env, jti string) error {
	client, err := cellaClient(a.apiURL)
	if err != nil {
		return err
	}
	return client.RevokeEnvironmentKey(ctx, env, jti)
}

func (a deviceAccount) Exists(ctx context.Context, env string) (bool, error) {
	client, err := cellaClient(a.apiURL)
	if err != nil {
		return false, err
	}
	_, _, err = client.GetEnvironment(ctx, env)
	if err == nil {
		return true, nil
	}
	if errors.Is(removedOr(err), devices.ErrRemoved) {
		return false, nil
	}
	return false, err
}

// removedOr marks a refusal that says the device is no longer the
// person's: Cella holds no such environment, or the platform refuses it as
// removed or as another person's.
func removedOr(err error) error {
	var ce *cellaclient.Error
	if !errors.As(err, &ce) {
		return err
	}
	switch {
	case ce.Code == "not_found":
	case ce.Code == "forbidden" && (platformReason(ce.Detail) == "device_removed" || platformReason(ce.Detail) == "device_not_owned"):
	default:
		return err
	}
	return fmt.Errorf("%w: %w", devices.ErrRemoved, err)
}

// defaultComputerName is the computer's own name: the one macOS shows in
// its sharing settings, or the host name without its local domain.
func defaultComputerName() string {
	if goruntime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "ComputerName").Output(); err == nil {
			if name := strings.TrimSpace(string(out)); name != "" {
				return name
			}
		}
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "This computer"
	}
	return strings.TrimSuffix(host, ".local")
}

// cleanDeviceName keeps a name the platform takes: printable characters,
// trimmed, at most maxDeviceName of them.
func cleanDeviceName(name string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(name) {
		if !unicode.IsPrint(r) {
			continue
		}
		if n == maxDeviceName {
			break
		}
		b.WriteRune(r)
		n++
	}
	if out := strings.TrimSpace(b.String()); out != "" {
		return out
	}
	return "This computer"
}
