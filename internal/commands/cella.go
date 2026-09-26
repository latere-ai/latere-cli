// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	cellaclient "latere.ai/x/cella/client"
	v1 "latere.ai/x/cella/manifest/v1"
	"latere.ai/x/pkg/otel"

	"github.com/latere-ai/latere-cli/internal/api"
)

// defaultCellaURL is the hosted Cella control plane: its address under the
// platform origin, including the base path the exported client maps every
// /v1 route onto.
const defaultCellaURL = "https://api.latere.ai/v1/environments"

// cellaURLUsage is the --api-url help every Cella command shares. The base
// carries the path the routes sit under, so a bare host is not enough.
const cellaURLUsage = "Cella API base URL, including its /v1/environments path (default " + defaultCellaURL + ", or $LATERE_CELLA_URL)"

// cellaAudience is the aud claim of the bearer every Cella command presents:
// the control plane's own audience, which auth mints actor tokens for on the
// latere-cli client. It is the production audience whatever --api-url names:
// the URL selects the deployment, the audience is what the issuer stamps.
const cellaAudience = "cella"

// cellaWorkspace is where a sandbox's workspace is mounted unless its
// manifest moves it. A relative path given to a file command is resolved
// under it, because the control plane takes absolute paths alone.
const cellaWorkspace = "/workspace"

// cellaCreateHold is how long a create is held for the sandbox to start: the
// control plane's own default and the bound of `apply --wait` with no value.
const cellaCreateHold = 10 * time.Minute

// cellaTokenMargin is how long before its expiry a held bearer is replaced,
// enough for the request it is attached to to reach the control plane first.
const cellaTokenMargin = 60 * time.Second

// cellaUnknownExpiry is how long a bearer whose expiry the issuer did not
// state is reused before another is minted.
const cellaUnknownExpiry = time.Minute

// The phases of a Cella sandbox the commands decide on.
const (
	cellaPending  = "Pending"
	cellaQueued   = "Queued"
	cellaStarting = "Starting"
	cellaRunning  = "Running"
	cellaFailed   = "Failed"
	cellaLost     = "Lost"
)

// newCellaCmd is the canonical `latere cella …` command tree. The API's
// resource is the sandbox, and the product is Cella, so the CLI follows the
// product. `latere sandbox …` is kept as an alias.
func newCellaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cella",
		Aliases: []string{"sandbox"},
		Short:   "Manage cellas: create, list, run commands in, and move files in or out.",
		Long: `Manage Cella sandboxes on the Latere platform.

A cella is a sandbox with a persistent workspace at /workspace. It runs on
the Cella control plane at https://api.latere.ai/v1/environments, reaches
only the hosts its egress boundary admits, and runs an image from the
platform's catalog: base, or gui for a desktop.

A create answers as soon as the sandbox is recorded, usually Pending.
'latere cella apply --wait' holds the answer until it runs or fails.`,
		Example: `  latere cella apply -f sandbox.yaml --wait
  latere cella list
  latere cella exec dev -- uname -a
  latere cella shell dev
  latere cella run --ephemeral --rm -- python -c 'print(1)'
  latere cella export dev src -o workspace.tar`,
		// The group runs only for a word that names none of its commands,
		// to say why a command of the retired API is gone. Its flags are
		// not the group's to refuse first.
		Args:               cobra.ArbitraryArgs,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if reason, ok := removedCellaCommands[args[0]]; ok {
				return fmt.Errorf("'latere cella %s' is no longer available: %s", args[0], reason)
			}
			return fmt.Errorf("unknown command %q for %q; 'latere cella --help' lists the commands", args[0], cmd.CommandPath())
		},
	}
	cmd.AddCommand(
		newCeApplyCmd(),
		newCeListCmd(),
		newCeGetCmd(),
		newCeStartCmd(),
		newCeStopCmd(),
		newCeDeleteCmd(),
		newCeExecCmd(),
		newCeShellCmd(),
		newCeRunCmd(),
		newCeLogsCmd(),
		newCeImportCmd(),
		newCeExportCmd(),
		newCeCatCmd(),
		newCeWriteCmd(),
		newCeLsCmd(),
		newCeUploadCmd(),
		newCeMkdirCmd(),
		newCeRmCmd(),
		newCeMvCmd(),
	)
	return cmd
}

// removedCellaCommands are the commands of the retired hosted sandbox API
// that the Cella core has no counterpart for, each with the reason a user
// reads when they run it.
var removedCellaCommands = map[string]string{
	"policy":  "the Cella core has no named policy profiles; a sandbox's egress boundary is spec.network.egress in its manifest",
	"rename":  "a sandbox's name is fixed when it is created",
	"extend":  "the Cella core has no tiers or deadlines; set spec.lifecycle (autoStop, ttl, autoDelete) in the manifest you create the sandbox from",
	"convert": "the Cella core has no tiers or deadlines; set spec.lifecycle (autoStop, ttl, autoDelete) in the manifest you create the sandbox from",
	"resize":  "a sandbox's resources are fixed when it is created; apply a new sandbox with the resources it needs",
	"wait":    "the Cella core runs a command to completion and keeps no command records; 'latere cella exec' runs one and waits for it",
}

// ---- the client ----

// resolveCellaURL returns the control plane's address: the flag, then
// $LATERE_CELLA_URL, then the hosted control plane.
func resolveCellaURL(flagURL string) string {
	u := flagURL
	if u == "" {
		u = os.Getenv("LATERE_CELLA_URL")
	}
	if u == "" {
		u = defaultCellaURL
	}
	return strings.TrimRight(u, "/")
}

// cellaHTTPClient carries every Cella request, the attach socket included.
// It has no Timeout: the exported client's own transport allows ten seconds
// to the first response byte, and a create held until the sandbox runs, like
// a synchronous command, answers only when it is done. Deadlines come from
// the command's context instead.
func cellaHTTPClient() *http.Client {
	return &http.Client{Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}
}

// cellaClient builds the Cella client for one command run. The bearer is an
// actor token minted for cellaAudience alone from the saved login, so the
// login token never reaches Cella. The client asks for it once per request,
// and the token is re-minted when the held one is within cellaTokenMargin of
// its expiry, which a transfer, an attach or a log follow outlives.
//
// LATERE_CELLA_TOKEN presents a bearer as given, for a development
// deployment or a test, the same escape every other product has.
func cellaClient(apiURL string) (*cellaclient.Client, error) {
	base := resolveCellaURL(apiURL)
	var token cellaclient.TokenSource
	if t := strings.TrimSpace(os.Getenv("LATERE_CELLA_TOKEN")); t != "" {
		token = cellaclient.StaticToken(t)
	} else {
		token = mintedCellaToken(api.ResolveAuthURL(base, ""))
	}
	return cellaclient.New(cellaclient.Config{
		URL:        base,
		Token:      token,
		HTTPClient: cellaHTTPClient(),
		UserAgent:  "latere-cli",
	})
}

// mintedCellaToken is a token source that mints an actor token at authBase on
// first use and again whenever the held one is due.
func mintedCellaToken(authBase string) cellaclient.TokenSource {
	var (
		mu     sync.Mutex
		held   string
		expiry time.Time
	)
	return cellaclient.TokenFunc(func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if held != "" && time.Now().Before(expiry.Add(-cellaTokenMargin)) {
			return held, nil
		}
		token, exp, err := api.ActorToken(ctx, authBase, cellaAudience)
		if err != nil {
			return "", fmt.Errorf("cannot authenticate to Cella: %w", err)
		}
		if exp.IsZero() {
			exp = time.Now().Add(cellaUnknownExpiry + cellaTokenMargin)
		}
		held, expiry = token, exp
		return held, nil
	})
}

// resolveCellaPath maps a path argument onto the control plane's rule,
// which takes only absolute paths at or below the workspace. A relative path
// is resolved under the workspace; an absolute one is sent as given.
func resolveCellaPath(p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(cellaWorkspace, p)
}

// ---- apply / list / get / start / stop / delete ----

// newCeApplyCmd registers `latere cella apply -f <file>`. The manifest is
// sent as written: the control plane decodes JSON and YAML alike, strictly,
// and is the authoritative validator. A manifest that names its sandbox is
// applied under that name, so applying it again updates the sandbox rather
// than creating a second one.
func newCeApplyCmd() *cobra.Command {
	var (
		file   string
		apiURL string
		wait   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create or update a cella from a Sandbox manifest.",
		Long: `Create a cella from a declarative Sandbox manifest, or update the one
it names.

A manifest in YAML or JSON:

  apiVersion: cella.latere.ai/v1beta1
  kind: Sandbox
  metadata:
    name: dev                       # Optional. The server names it if omitted.
  spec:
    image: base                     # base, or gui for a desktop.
    resources: {cpu: "2", memory: 4Gi, disk: 20Gi}
    network:
      egress:
        allowedHosts: [api.latere.ai, github.com]
    lifecycle:
      autoStop: 15m                 # Stop after this much idle time.

The answer is the sandbox as soon as it is recorded, usually Pending.
--wait holds it until the sandbox runs or fails, ten minutes unless
--wait=DURATION says otherwise, and a sandbox that fails exits 1 with
its reason.

Field reference: https://platform.latere.ai/docs/cella/manifest`,
		Example: `  latere cella apply -f sandbox.yaml
  latere cella apply -f sandbox.yaml --wait
  cat sandbox.json | latere cella apply -f - --wait=2m`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("-f is required (path to a Sandbox manifest, or - for stdin)")
			}
			held := cmd.Flags().Changed("wait")
			if held && (wait <= 0 || wait > time.Hour) {
				return fmt.Errorf("--wait must be positive and at most 1h")
			}
			body, err := readManifestBody(file, cmd.InOrStdin())
			if err != nil {
				return err
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			var opts []cellaclient.CreateOption
			if held {
				opts = append(opts, cellaclient.Wait(wait))
			}
			m := manifestOf(body)
			var sb v1.Sandbox
			if name := manifestName(body); name != "" {
				sb, _, err = c.ApplySandbox(cmd.Context(), name, m, opts...)
			} else {
				sb, _, err = c.CreateSandbox(cmd.Context(), m, opts...)
			}
			if err != nil {
				return err
			}
			if err := printSandbox(cmd.OutOrStdout(), sb); err != nil {
				return err
			}
			if err := startFailure(sb); err != nil && held {
				return err
			}
			name, phase := sb.Metadata.Name, sb.Status.Phase
			switch {
			case !starting(phase):
			case held:
				fprintf(cmd.ErrOrStderr(), "%s is still %s after the hold; 'latere cella get %s' reads its phase\n", name, phase, name)
			default:
				fprintf(cmd.ErrOrStderr(), "%s is %s; 'latere cella get %s' reads its phase, and 'apply --wait' holds the create until it runs\n", name, phase, name)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "path to a Sandbox manifest in YAML or JSON, or - for stdin")
	_ = cmd.MarkFlagRequired("file")
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.DurationVarP(&wait, "wait", "w", 0, "hold the create until the sandbox runs or fails, at most this long (default 10m)")
	f.Lookup("wait").NoOptDefVal = cellaCreateHold.String()
	return cmd
}

// manifestOf is the manifest in the syntax it was written in. A body whose
// first character is a brace is JSON; anything else is YAML, of which JSON is
// also a subset, so the control plane reads either way.
func manifestOf(body []byte) cellaclient.Manifest {
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		return cellaclient.JSON(body)
	}
	return cellaclient.YAML(body)
}

// manifestName is the sandbox name the manifest declares, or empty when it
// declares none or cannot be read here. An unreadable manifest is sent as a
// create, so the refusal the user reads is the control plane's.
func manifestName(body []byte) string {
	var m struct {
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(body, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Metadata.Name)
}

// starting reports whether a sandbox is still on its way to running.
func starting(phase string) bool {
	return phase == cellaPending || phase == cellaQueued || phase == cellaStarting
}

// startFailure is the error of a held create whose sandbox did not start: a
// Failed or Lost sandbox names its reason. A sandbox still starting when the
// hold ended is not a failure; its phase is already printed.
func startFailure(sb v1.Sandbox) error {
	switch sb.Status.Phase {
	case cellaFailed, cellaLost:
		return fmt.Errorf("cella %s did not start: phase %s, reason %s", sb.Metadata.Name, sb.Status.Phase, defaultStr(sb.Status.Reason, "not given"))
	}
	return nil
}

// readManifestBody reads the manifest from path or, if path is "-",
// from the supplied stdin reader. Capped at 64 KiB to match the server's body limit.
func readManifestBody(path string, stdin io.Reader) ([]byte, error) {
	return readManifestBodyWithLimit(path, stdin, 64<<10)
}

func readManifestBodyWithLimit(path string, stdin io.Reader, maxBytes int) ([]byte, error) {
	var r io.Reader
	if path == "-" {
		r = stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open manifest %q: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	body, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("manifest exceeds %d byte limit", maxBytes)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("manifest is empty")
	}
	return body, nil
}

func newCeListCmd() *cobra.Command {
	var (
		apiURL string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your cellas.",
		Long:  "List the cellas the current login may read, with each one's phase.",
		Example: `  latere cella list
  latere cella list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			sbs, raws, err := c.ListSandboxes(cmd.Context(), cellaclient.ListOptions{})
			if err != nil {
				return err
			}
			if jsonF {
				if raws == nil {
					raws = []json.RawMessage{}
				}
				return printJSON(cmd.OutOrStdout(), raws)
			}
			return printSandboxList(cmd.OutOrStdout(), sbs)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func newCeGetCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "get <name|id>",
		Short: "Get a cella by name or id.",
		Long:  "Fetch one cella by name or id and print it as the control plane answered, in JSON.",
		Example: `  latere cella get dev
  latere cella get sbx_01k5x6j9c2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			_, raw, err := c.GetSandbox(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printRawJSON(cmd.OutOrStdout(), raw)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// printRawJSON writes the control plane's own bytes, indented.
func printRawJSON(out io.Writer, raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("the answer is not JSON: %w", err)
	}
	buf.WriteByte('\n')
	_, err := out.Write(buf.Bytes())
	return err
}

func newCeStartCmd() *cobra.Command { return simpleAction("start", "Start a stopped cella.") }
func newCeStopCmd() *cobra.Command  { return simpleAction("stop", "Stop a running cella.") }

func simpleAction(verb, short string) *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   verb + " <name|id>",
		Short: short,
		Long: fmt.Sprintf("%s a cella by name or id. The workspace is kept across a stop and a start.",
			strings.ToUpper(verb[:1])+verb[1:]),
		Example: fmt.Sprintf(`  latere cella %s dev
  latere cella %s sbx_01k5x6j9c2`, verb, verb),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			act := c.StartSandbox
			if verb == "stop" {
				act = c.StopSandbox
			}
			sb, _, err := act(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printSandbox(cmd.OutOrStdout(), sb)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

func newCeDeleteCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a cella (workspace contents are lost).",
		Long: `Delete a cella and its workspace.

This removes the workspace. Export files first if you need to keep them.`,
		Example: `  latere cella export dev -o dev.tar
  latere cella delete dev`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			if _, err := c.Delete(cmd.Context(), cellaclient.KindSandbox, args[0]); err != nil {
				return err
			}
			fprintf(cmd.ErrOrStderr(), "deleted %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// ---- exec / run / logs ----

// newCeExecCmd registers `latere cella exec`: one command in an existing
// cella, run to completion on the control plane's synchronous route.
func newCeExecCmd() *cobra.Command {
	var (
		apiURL  string
		envFlag []string
		cwd     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "exec <name|id> -- <cmd>...",
		Short: "Run a command in a cella and wait for it.",
		Long: `Run a command in an existing cella and wait for it to end.

The command's standard output and standard error are written to yours
when it ends, each cut at one mebibyte, and the CLI exits with the
command's exit code. Its standard input is empty. For an interactive
program, open a terminal with 'latere cella shell'.`,
		Example: `  latere cella exec dev -- uname -a
  latere cella exec dev --cwd app --env DEBUG=1 -- python -m pytest
  latere cella exec dev --timeout 30m -- make build`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := cellaExecRequest(args[1:], envFlag, cwd, timeout)
			if err != nil {
				return err
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			res, _, err := c.Exec(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			return writeExecResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringArrayVar(&envFlag, "env", nil, "environment variable KEY=VALUE for this command; repeatable")
	f.StringVar(&cwd, "cwd", "", "working directory; a relative path is under /workspace")
	f.DurationVar(&timeout, "timeout", 0, "end the command after this long, at most 1h (default 10m)")
	return cmd
}

// cellaExecRequest is the body of one exec. The timeout is the control
// plane's to enforce, which refuses one above an hour.
func cellaExecRequest(argv, envFlag []string, cwd string, timeout time.Duration) (cellaclient.ExecRequest, error) {
	if timeout < 0 || timeout > time.Hour {
		return cellaclient.ExecRequest{}, fmt.Errorf("--timeout must be positive and at most 1h")
	}
	env, err := parseKV(envFlag)
	if err != nil {
		return cellaclient.ExecRequest{}, err
	}
	req := cellaclient.ExecRequest{Command: argv, Env: env}
	if cwd != "" {
		req.Workdir = resolveCellaPath(cwd)
	}
	if timeout > 0 {
		req.Timeout = timeout.String()
	}
	return req, nil
}

// writeExecResult writes a finished command's two outputs to the CLI's own
// and returns its exit code as the command's result.
func writeExecResult(stdout, stderr io.Writer, res cellaclient.ExecResult) error {
	if _, err := io.WriteString(stdout, res.Stdout); err != nil {
		return fmt.Errorf("write command stdout: %w", err)
	}
	if _, err := io.WriteString(stderr, res.Stderr); err != nil {
		return fmt.Errorf("write command stderr: %w", err)
	}
	if res.Truncated {
		fprintln(stderr, "cella: the output was cut at the control plane's one mebibyte cap")
	}
	return remoteExit(res.ExitCode)
}

// newCeRunCmd registers `latere cella run --ephemeral --rm`: a disposable
// cella created for one command and deleted after it.
func newCeRunCmd() *cobra.Command {
	var (
		apiURL    string
		envFlag   []string
		cwd       string
		ephemeral bool
		rm        bool
		image     string
		diskGB    int
		cpu       string
		memory    string
		timeout   int
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "run --ephemeral --rm -- <argv>...",
		Short: "Run one command in a disposable cella that is deleted after it.",
		Long: `Run one command in a disposable cella.

Cella creates a sandbox for this command, waits for it to run, runs the
command, and deletes the sandbox when the command ends, fails, or is
interrupted. Both --ephemeral and --rm are required, so the deletion is
never implied. To run a command in a cella you keep, use
'latere cella exec'.

The sandbox takes the platform's default egress boundary. It also stops
after 15 minutes idle and is deleted two hours after its creation, so
one the CLI could not delete does not linger.`,
		Example: `  latere cella run --ephemeral --rm -- python -c 'print("hello")'
  latere cella run --ephemeral --rm --cpu 2 --memory 4Gi -- make test
  latere cella run --ephemeral --rm --json -- uname -a`,
		Args: func(cmd *cobra.Command, args []string) error {
			if !ephemeral || !rm {
				return fmt.Errorf("run needs --ephemeral --rm; to run a command in an existing cella, use 'latere cella exec'")
			}
			if len(args) == 0 {
				return fmt.Errorf("missing argv after --")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 || timeout > 3600 {
				return fmt.Errorf("--timeout must be between 1 and 3600 seconds")
			}
			if diskGB < 0 {
				return fmt.Errorf("--disk must not be negative")
			}
			req, err := cellaExecRequest(args, envFlag, cwd, time.Duration(timeout)*time.Second)
			if err != nil {
				return err
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			spec := oneShotSpec(image, cpu, memory, diskGB)
			return runOneShot(cmd.Context(), c, spec, req, jsonOut, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringArrayVar(&envFlag, "env", nil, "environment variable KEY=VALUE for the command; repeatable")
	f.StringVar(&cwd, "cwd", "", "working directory; a relative path is under /workspace")
	f.BoolVar(&ephemeral, "ephemeral", false, "create a disposable cella for this command; required")
	f.BoolVar(&rm, "rm", false, "delete the cella after the command; required")
	f.StringVar(&image, "image", "", "catalog image: base (the default) or gui")
	f.IntVar(&diskGB, "disk", 0, "workspace size in GiB (default: the platform's)")
	f.StringVar(&cpu, "cpu", "", "CPU limit as a Kubernetes quantity, e.g. 1.5 or 1500m")
	f.StringVar(&memory, "memory", "", "memory limit as a Kubernetes quantity, e.g. 4Gi or 2048Mi")
	f.IntVar(&timeout, "timeout", 600, "command timeout in seconds, at most 3600")
	f.BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// oneShotSpec is the manifest of a disposable cella. The lifecycle rules are
// the backstop for a sandbox the CLI could not delete, a process killed
// between the create and the delete among them.
func oneShotSpec(image, cpu, memory string, diskGB int) v1.Sandbox {
	sb := v1.Sandbox{
		APIVersion: v1.APIVersion,
		Kind:       v1.KindSandbox,
		Spec: v1.SandboxSpec{
			Image:     image,
			Resources: v1.Resources{CPU: v1.Quantity(cpu), Memory: v1.Quantity(memory)},
			Lifecycle: v1.Lifecycle{AutoStop: "15m", TTL: "2h"},
		},
	}
	if diskGB > 0 {
		sb.Spec.Resources.Disk = v1.Quantity(fmt.Sprintf("%dGi", diskGB))
	}
	return sb
}

// oneShotName is the name a disposable cella is created under. The CLI picks
// it, rather than the control plane, so a create whose answer never arrives,
// because the hold was interrupted or the connection dropped, still leaves a
// name to delete. Twelve base32 characters make a collision with another of
// the caller's sandboxes, which the apply would update, negligible.
func oneShotName() string {
	return "run-" + strings.ToLower(rand.Text()[:12])
}

// oneShotResult is what `run --json` prints.
type oneShotResult struct {
	Sandbox    string `json:"sandbox"`
	ID         string `json:"id"`
	ExitCode   int    `json:"exitCode"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	CreateMS   int64  `json:"createMs"`
	DurationMS int64  `json:"durationMs"`
}

// runOneShot is the create-then-use flow: the create is held until the
// sandbox runs, the command runs to completion, and the sandbox is deleted on
// every path after the create, a failed start and an interrupt included. The
// delete runs on a context detached from the command's, which an interrupt
// has already ended.
func runOneShot(ctx context.Context, c *cellaclient.Client, spec v1.Sandbox, req cellaclient.ExecRequest, jsonOut bool, stdout, stderr io.Writer) (err error) {
	name := oneShotName()
	spec.Metadata.Name = name
	m, err := cellaclient.Encode(spec)
	if err != nil {
		return err
	}
	began := time.Now()
	sb, _, err := c.ApplySandbox(ctx, name, m, cellaclient.Wait(cellaCreateHold))
	if err != nil {
		// A refusal created nothing. Any other failure may have lost the
		// answer to a create the control plane recorded, so the name is
		// deleted, and a sandbox that was never made reads as not found.
		if cellaclient.CodeOf(err) == "" {
			if delErr := deleteOneShot(ctx, c, name, jsonOut, stderr); delErr != nil {
				err = errors.Join(err, delErr)
			}
		}
		return err
	}
	created := time.Since(began)
	defer func() {
		delErr := deleteOneShot(ctx, c, name, jsonOut, stderr)
		if delErr == nil {
			return
		}
		// A command's own exit code stays the process's, which leaves the
		// error unprinted, so a cella left behind is reported here instead.
		if _, remote := errors.AsType[*remoteExitError](err); remote {
			fprintln(stderr, delErr)
			return
		}
		err = errors.Join(err, delErr)
	}()
	if sb.Status.Phase != cellaRunning {
		if failure := startFailure(sb); failure != nil {
			return failure
		}
		return fmt.Errorf("cella %s is still %s after %s", name, sb.Status.Phase, cellaCreateHold)
	}
	if !jsonOut {
		fprintf(stderr, "created cella %s in %s\n", name, created.Round(time.Millisecond))
	}
	res, _, err := c.Exec(ctx, name, req)
	if err != nil {
		return err
	}
	if jsonOut {
		if err := printJSON(stdout, oneShotResult{
			Sandbox: name, ID: sb.Status.ID, ExitCode: res.ExitCode,
			Stdout: res.Stdout, Stderr: res.Stderr, Truncated: res.Truncated,
			CreateMS: created.Milliseconds(), DurationMS: res.DurationMS,
		}); err != nil {
			return err
		}
		return remoteExit(res.ExitCode)
	}
	return writeExecResult(stdout, stderr, res)
}

// deleteOneShot deletes a disposable cella by name on a context of its own,
// bounded to a minute, so an interrupt that ended the command's context does
// not also cancel the cleanup. A cella already gone is no failure. quiet
// leaves out the confirmation line, which a --json output keeps off stderr.
func deleteOneShot(ctx context.Context, c *cellaclient.Client, name string, quiet bool, stderr io.Writer) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if _, err := c.Delete(cleanup, cellaclient.KindSandbox, name); err != nil {
		if cellaclient.CodeOf(err) == "not_found" {
			return nil
		}
		return fmt.Errorf("delete cella %s: %w; delete it with 'latere cella delete %s'", name, err, name)
	}
	if !quiet {
		fprintf(stderr, "deleted cella %s\n", name)
	}
	return nil
}

func newCeLogsCmd() *cobra.Command {
	var (
		apiURL string
		follow bool
		tail   int
		since  string
	)
	cmd := &cobra.Command{
		Use:   "logs <name|id>",
		Short: "Read or follow a cella's main process output.",
		Long: `Read the output of a cella's main process, the command its manifest
runs. --follow keeps writing it as it arrives.`,
		Example: `  latere cella logs dev
  latere cella logs dev --tail 100
  latere cella logs dev --follow --since 2026-09-26T10:00:00Z`,
		Args: func(cmd *cobra.Command, args []string) error {
			// A second argument was a command id on the retired API.
			if len(args) == 2 {
				return errors.New("logs reads a cella's main process output and takes no command id: the Cella core keeps no command records")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if tail < 0 {
				return fmt.Errorf("--tail must not be negative")
			}
			opts := cellaclient.LogOptions{Follow: follow, Tail: tail}
			if since != "" {
				t, err := time.Parse(time.RFC3339, since)
				if err != nil {
					return fmt.Errorf("--since must be RFC3339: %w", err)
				}
				opts.Since = t
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			body, err := c.Logs(cmd.Context(), args[0], opts)
			if err != nil {
				return err
			}
			defer func() { _ = body.Close() }()
			if _, err := io.Copy(cmd.OutOrStdout(), body); err != nil {
				return fmt.Errorf("write logs: %w", err)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.BoolVarP(&follow, "follow", "f", false, "keep writing output as it arrives")
	f.IntVar(&tail, "tail", 0, "start this many lines from the end")
	f.StringVar(&since, "since", "", "only output after this RFC3339 instant")
	return cmd
}

// ---- output ----

// parseKV turns ["KEY=VALUE", ...] into a map.
func parseKV(items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, kv := range items {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid env %q (want KEY=VALUE)", kv)
		}
		m[k] = v
	}
	return m, nil
}

func printSandboxList(out io.Writer, sbs []v1.Sandbox) error {
	if len(sbs) == 0 {
		if _, err := fmt.Fprintln(out, "No cellas are visible to this login."); err != nil {
			return fmt.Errorf("write cella list: %w", err)
		}
		return nil
	}
	for i, s := range sbs {
		if i > 0 {
			if _, err := fmt.Fprintln(out); err != nil {
				return fmt.Errorf("write cella list separator: %w", err)
			}
		}
		if err := printSandbox(out, s); err != nil {
			return err
		}
	}
	return nil
}

// printSandbox writes one cella as a record: what it is, its phase and why,
// and what it runs.
func printSandbox(out io.Writer, s v1.Sandbox) error {
	var record strings.Builder
	field := func(label, value string) {
		record.WriteString(formatWrappedField(label, value))
	}
	field("cella", defaultStr(s.Metadata.Name, "-"))
	field("id", s.Status.ID)
	field("phase", s.Status.Phase)
	field("reason", s.Status.Reason)
	field("image", s.Spec.Image)
	field("resources", sandboxResourceSummary(s.Spec.Resources))
	if !s.Status.CreatedAt.IsZero() {
		field("created", humanAge(s.Status.CreatedAt)+" ago")
	}
	if !s.Status.ExpiresAt.IsZero() {
		field("expires", s.Status.ExpiresAt.UTC().Format(time.RFC3339))
	}
	for _, w := range s.Status.Warnings {
		field("warning", w)
	}
	if _, err := fmt.Fprint(out, record.String()); err != nil {
		return fmt.Errorf("write cella details for %q: %w", s.Status.ID, err)
	}
	return nil
}

// sandboxResourceSummary renders the resources a manifest resolved to, in the
// quantities it was written in. Empty when none is set.
func sandboxResourceSummary(r v1.Resources) string {
	var parts []string
	for _, p := range []struct {
		name  string
		value v1.Quantity
	}{{"cpu", r.CPU}, {"memory", r.Memory}, {"disk", r.Disk}} {
		if p.value != "" {
			parts = append(parts, p.name+"="+string(p.value))
		}
	}
	return strings.Join(parts, " ")
}
