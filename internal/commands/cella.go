// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"

	"latere.ai/x/pkg/relpath"
)

// ---- DTOs (subset of sandboxd's OpenAPI; keep loose so additive
//      backend changes don't break the CLI). ----

type sandboxDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	State           string            `json:"state"`
	Tier            string            `json:"tier,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	LastActivityAt  time.Time         `json:"last_activity_at,omitzero"`
	AutoStopMinutes int               `json:"auto_stop_minutes,omitempty"`
	DiskGB          int               `json:"disk_gb,omitempty"`
	CPUMilli        int               `json:"cpu_milli,omitempty"`
	MemoryMB        int               `json:"memory_mb,omitempty"`
	Deadline        time.Time         `json:"deadline,omitzero"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	Workdir         string            `json:"workdir,omitempty"`
}

type policyDTO struct {
	Name               string    `json:"name"`
	Label              string    `json:"label"`
	Description        string    `json:"description"`
	CapabilityProfile  string    `json:"capability_profile"`
	SidecarRequired    bool      `json:"sidecar_required"`
	IsDefault          bool      `json:"is_default"`
	Selectable         bool      `json:"selectable"`
	AssignmentSource   string    `json:"assignment_source"`
	NetworkEgressFQDNs []string  `json:"network_egress_fqdns,omitempty"`
	CreatedAt          time.Time `json:"created_at,omitzero"`
	UpdatedAt          time.Time `json:"updated_at,omitzero"`
}

type commandDTO struct {
	CommandID string    `json:"command_id"`
	Phase     string    `json:"phase"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
	ExitedAt  time.Time `json:"exited_at,omitzero"`
}

type logsCursorDTO struct {
	Bytes      string `json:"bytes"`
	NextCursor int64  `json:"next_cursor"`
	Phase      string `json:"phase"`
	ExitCode   *int   `json:"exit_code,omitempty"`
}

type oneShotRunDTO struct {
	RunID       string            `json:"run_id"`
	SandboxID   string            `json:"sandbox_id"`
	SandboxName string            `json:"sandbox_name"`
	State       string            `json:"state"`
	ExitCode    *int              `json:"exit_code,omitempty"`
	Links       map[string]string `json:"links,omitempty"`
	Timing      struct {
		CreateMS  int64 `json:"create_ms"`
		ExecMS    int64 `json:"exec_ms"`
		CleanupMS int64 `json:"cleanup_ms"`
		TotalMS   int64 `json:"total_ms"`
	} `json:"timing"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	Truncated    bool   `json:"truncated"`
	Error        string `json:"error,omitempty"`
	CleanupError string `json:"cleanup_error,omitempty"`
}

// ---- top-level ----

// newCellaCmd is the canonical `latere cella …` command tree. The
// underlying API resource is "sandbox", but the product brand — and
// the matching surface on https://latere.ai/cella — is Cella, so the
// CLI follows the brand. `latere sandbox …` stays as a hidden alias
// for v0.1.0 compatibility.
func newCellaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cella",
		Aliases: []string{"sandbox"},
		Short:   "Manage cellas (create, list, policy, start, stop, delete, run).",
		Long: `Manage Cella sandboxes — per-user compute environments at cella.latere.ai.

Each cella is a PVC-backed workspace plus a Pod for compute. Tier
'ephemeral' auto-stops on idle and auto-deletes after a wall-clock
window; tier 'persistent' stays until you delete it.`,
		Example: `  latere cella list
  latere cella policy list
  latere cella apply -f sandbox.yaml
  latere cella shell dev
  latere cella run dev -- python train.py
  latere cella export dev src -o workspace.tar`,
	}
	cmd.AddCommand(
		newCeApplyCmd(),
		newCeListCmd(),
		newCeGetCmd(),
		newCeRenameCmd(),
		newCeStartCmd(),
		newCeStopCmd(),
		newCeDeleteCmd(),
		newCePolicyCmd(),
		newCeExecCmd(),
		newCeShellCmd(),
		newCeRunCmd(),
		newCeLogsCmd(),
		newCeWaitCmd(),
		newCeImportCmd(),
		newCeExportCmd(),
		newCeExtendCmd(),
		newCeConvertCmd(),
		newCeResizeCmd(),
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

// newCeExecCmd registers `latere cella exec`, the synchronous
// streaming variant of `cella run`.
func newCeExecCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "exec <name|id> -- <cmd>...",
		Short: "Run a command synchronously inside a cella (streams logs to stdout).",
		Long: `Run a command synchronously inside an existing cella.

Stdout and stderr are streamed to your terminal. The CLI exits with
the remote command's exit code when the command finishes.`,
		Example: `  latere cella exec dev -- uname -a
  latere cella exec dev -- python -m pytest
  latere cella exec sb-019dc976-2b28-7c55-8778-bf7d5ae6c58d -- env`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			sandbox := args[0]
			argv := args[1:]
			return runAndStream(cmd.Context(), c, sandbox, argv, nil, "", nil)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

// ---- apply / list / get / rename / start / stop / delete ----

// newCeApplyCmd registers `latere cella apply -f <file>`. Reads the
// SandboxManifest from disk and POSTs the raw bytes to
// /v1/sandboxes with Content-Type: application/yaml. The server is
// the authoritative validator, so the CLI does no schema work.
// "-" reads the manifest from stdin.
func newCeApplyCmd() *cobra.Command {
	var (
		file    string
		apiURL  string
		idemKey string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create a cella from a Sandbox Manifest file.",
		Long: `Create a Cella from a declarative Sandbox Manifest.

The same YAML accepted by the dashboard's YAML tab and the
public API. Defaults hit the warm pool, so a Manifest like the
one below starts in around 300 ms:

  apiVersion: cella.latere.ai/v1        # Schema version.
  kind: Sandbox
  metadata:
    name: dev                           # Optional. Server picks one if omitted.
  spec:
    image: ghcr.io/latere-ai/sandbox-base:latest
    tier: ephemeral                     # Or "persistent" to keep the workspace.
    lifecycle:
      autoStop: 15m                     # Stop the compute after this much idle.

Full field reference: https://cella.latere.ai/docs/cella/manifest`,
		Example: `  latere cella apply -f sandbox.yaml
  cat sandbox.yaml | latere cella apply -f -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("-f is required (path to a Sandbox Manifest, or - for stdin)")
			}
			body, err := readManifestBody(file)
			if err != nil {
				return err
			}
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			var headers map[string]string
			if idemKey != "" {
				headers = map[string]string{"Idempotency-Key": idemKey}
			}
			if err := c.DoWithHeaders(cmd.Context(), http.MethodPost, "/v1/sandboxes",
				bytes.NewReader(body), "application/yaml", headers, &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "path to a Sandbox Manifest YAML file, or - for stdin")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	cmd.Flags().StringVar(&idemKey, "idempotency-key", "", "dedup retried creates; same key + body replays the original result")
	return cmd
}

// readManifestBody reads the manifest from path or, if path is "-",
// from stdin. Capped at 64 KiB to match the server's body limit.
func readManifestBody(path string) ([]byte, error) {
	const maxBytes = 64 << 10
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open manifest %q: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
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
		Long: `List cellas available to the current token.

Regular users see their own cellas. Superadmin tokens can see all
cellas returned by the backend, including warm-pool cellas.`,
		Example: `  latere cella list
  latere cella list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sbs []sandboxDTO
			if err := c.GetJSON(cmd.Context(), "/v1/sandboxes", &sbs); err != nil {
				return err
			}
			if jsonF {
				return printJSON(sbs)
			}
			if len(sbs) == 0 {
				fprintln(os.Stdout, "No cellas are visible to this token.")
				return nil
			}
			printSandboxList(sbs)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func newCePolicyCmd() *cobra.Command {
	var (
		apiURL string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:     "policy",
		Aliases: []string{"policies"},
		Short:   "List policy profiles available for new cellas.",
		Long: `List Cella policy profiles visible to the current token.

Policies control runtime capabilities such as network shape, workspace
layout, and whether Cella's credential sidecar is required. The default
policy is used when a Manifest's spec.policy is left empty.

Use a selectable policy by setting it in your Manifest:

  spec:
    policy: restricted-network

If create fails because the selected policy requires the sidecar, list
policies and choose a selectable policy where sidecar is "no", or ask
an admin to configure the sidecar client for your token.`,
		Example: `  latere cella policy
  latere cella policy list
  latere cella policies --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyList(cmd.Context(), apiURL, jsonF)
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.BoolVar(&jsonF, "json", false, "JSON output")

	list := &cobra.Command{
		Use:   "list",
		Short: "List policy profiles available for new cellas.",
		Long:  cmd.Long,
		Example: `  latere cella policy list
  latere cella policy list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyList(cmd.Context(), apiURL, jsonF)
		},
	}
	list.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	list.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	cmd.AddCommand(list)
	return cmd
}

func runPolicyList(ctx context.Context, apiURL string, jsonF bool) error {
	c, err := authedClient(apiURL)
	if err != nil {
		return err
	}
	var policies []policyDTO
	if err := c.GetJSON(ctx, "/v1/policies", &policies); err != nil {
		return err
	}
	if jsonF {
		return printJSON(policies)
	}
	if len(policies) == 0 {
		fprintln(os.Stdout, "No policy profiles are visible to this token.")
		fprintln(os.Stdout, "Ask your Latere admin to assign a selectable policy, then re-run `latere cella apply` with `spec.policy` set in your Manifest.")
		return nil
	}
	printPolicies(policies)
	return nil
}

func printPolicies(policies []policyDTO) {
	for i, p := range policies {
		if i > 0 {
			fprintln(os.Stdout)
		}
		printWrappedField("policy", p.Name)
		printWrappedField("label", p.Label)
		printWrappedField("default", yesNo(p.IsDefault))
		printWrappedField("selectable", yesNo(p.Selectable))
		printWrappedField("sidecar", yesNo(p.SidecarRequired))
		printWrappedField("capability", defaultStr(p.CapabilityProfile, "-"))
		printWrappedField("source", defaultStr(p.AssignmentSource, "-"))
		printWrappedField("description", defaultStr(p.Description, "-"))
	}
}

func newCeGetCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "get <name|id>",
		Short: "Get a cella by name or id.",
		Long:  "Fetch one cella by slug or id and print the full JSON response.",
		Example: `  latere cella get dev
  latere cella get sb-019dc976-2b28-7c55-8778-bf7d5ae6c58d`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			if err := c.GetJSON(cmd.Context(), sbPath(args[0]), &sb); err != nil {
				return err
			}
			return printJSON(sb)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

func newCeRenameCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "rename <name|id> <new-name>",
		Short: "Rename a cella.",
		Long:  "Rename a cella slug while keeping the same underlying workspace and id.",
		Example: `  latere cella rename workspace-1 dev
  latere cella rename sb-019dc976-2b28-7c55-8778-bf7d5ae6c58d dev`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			body, err := jsonReader(map[string]any{"name": args[1]})
			if err != nil {
				return err
			}
			if err := c.Do(cmd.Context(), http.MethodPatch, sbPath(args[0]),
				body, "application/json", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

func newCeStartCmd() *cobra.Command { return simpleAction("start", "Start a stopped cella.") }
func newCeStopCmd() *cobra.Command  { return simpleAction("stop", "Stop a running cella.") }

func simpleAction(verb, short string) *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   verb + " <name|id>",
		Short: short,
		Long:  fmt.Sprintf("%s a cella by slug or id.", strings.ToUpper(verb[:1])+verb[1:]),
		Example: fmt.Sprintf(`  latere cella %s dev
  latere cella %s sb-019dc976-2b28-7c55-8778-bf7d5ae6c58d`, verb, verb),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			path := sbPath(args[0]) + "/" + verb
			if err := c.Do(cmd.Context(), http.MethodPost, path, nil, "", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

func newCeDeleteCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a cella (workspace contents are lost).",
		Long: `Delete a cella and its workspace data.

This removes the backing workspace. Export files first if you need to
keep them.`,
		Example: `  latere cella export dev -o dev.tar
  latere cella delete dev`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if err := c.Do(cmd.Context(), http.MethodDelete, sbPath(args[0]), nil, "", nil); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "deleted %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

// ---- run / logs / wait ----

func newCeRunCmd() *cobra.Command {
	var (
		apiURL         string
		envFlag        []string
		credentialFlag []string
		cwd            string
		follow         bool
		detach         bool
		ephemeral      bool
		rm             bool
		image          string
		diskGB         int
		cpu            string
		memory         string
		timeout        int
		printJSONOut   bool
	)
	cmd := &cobra.Command{
		Use:   "run [name|id] -- <argv>...",
		Short: "Run a command in a cella, or one-shot in a disposable ephemeral cella.",
		Long: `Run commands in Cella.

With a cella name or id, the command runs in that existing workspace.
By default the command starts in the background and prints a command id;
use --follow to stream logs and exit with the remote exit code.

With --ephemeral --rm, Cella creates a disposable workspace for this
single command and removes it when the command finishes. Add --detach
to start that one-shot run and return immediately with a run id.`,
		Example: `  latere cella run dev -- python train.py
  latere cella run dev --follow -- make test
  latere cella run dev --env DEBUG=1 --cwd /workspace/app -- npm test
  latere cella run --ephemeral --rm -- python -c 'print("hello")'
  latere cella run --ephemeral --rm --detach -- make benchmark`,
		Args: func(cmd *cobra.Command, args []string) error {
			if ephemeral || rm {
				if !ephemeral || !rm {
					return fmt.Errorf("--ephemeral and --rm must be used together for one-shot runs")
				}
				if len(args) == 0 {
					return fmt.Errorf("missing argv after --")
				}
				return nil
			}
			if detach {
				return fmt.Errorf("--detach requires --ephemeral --rm")
			}
			if len(args) < 2 {
				return fmt.Errorf("requires <name|id> -- <argv>... unless --ephemeral --rm is set")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			env, err := parseKV(envFlag)
			if err != nil {
				return err
			}
			if ephemeral && rm {
				if detach {
					out, err := oneShotRunDetached(cmd.Context(), c, args, env, cwd, image, diskGB, cpu, memory, timeout, credentialFlag)
					if err != nil {
						return err
					}
					if printJSONOut {
						return printJSON(out)
					}
					printStartedDetachedOneShotRun(out)
					return nil
				}
				out, err := oneShotRun(cmd.Context(), c, args, env, cwd, image, diskGB, cpu, memory, timeout, credentialFlag)
				if err != nil {
					return err
				}
				if printJSONOut {
					return printJSON(out)
				}
				printOneShotRun(out)
				if out.ExitCode != nil {
					os.Exit(*out.ExitCode)
				}
				return nil
			}
			if follow {
				return runAndStream(cmd.Context(), c, args[0], args[1:], env, cwd, credentialFlag)
			}
			cd, err := startCommand(cmd.Context(), c, args[0], args[1:], env, cwd, credentialFlag)
			if err != nil {
				return err
			}
			fmt.Println(cd.CommandID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.StringArrayVar(&envFlag, "env", nil, "non-secret KEY=VALUE; repeatable")
	f.StringArrayVar(&credentialFlag, "credential", nil, "trust-plane catalog key to use for this command; repeatable")
	f.StringVar(&cwd, "cwd", "", "working dir inside the cella")
	f.BoolVarP(&follow, "follow", "f", false, "stream logs and exit with the command's exit code")
	f.BoolVar(&detach, "detach", false, "start a disposable one-shot run and return its run id immediately")
	f.BoolVar(&ephemeral, "ephemeral", false, "create a disposable one-shot ephemeral cella for this command")
	f.BoolVar(&rm, "rm", false, "delete the one-shot cella after the command; required with --ephemeral")
	f.StringVar(&image, "image", "", "one-shot image ref (default Cella base image)")
	f.IntVar(&diskGB, "disk", 0, "one-shot PVC size in GB (default 1)")
	f.StringVar(&cpu, "cpu", "", "one-shot CPU limit as a Kubernetes quantity, e.g. 1.5 or 1500m")
	f.StringVar(&memory, "memory", "", "one-shot memory limit as a Kubernetes quantity, e.g. 4Gi or 2048Mi")
	f.IntVar(&timeout, "timeout", 600, "one-shot command timeout in seconds")
	f.BoolVar(&printJSONOut, "json", false, "print one-shot response as JSON")
	cmd.AddCommand(
		newCeRunStatusCmd(),
		newCeRunLogsCmd(),
		newCeRunCancelCmd(),
	)
	return cmd
}

func newCeRunStatusCmd() *cobra.Command {
	var (
		apiURL       string
		printJSONOut bool
	)
	cmd := &cobra.Command{
		Use:   "status <run_id>",
		Short: "Get a detached one-shot run status.",
		Long:  "Show the current phase, output summary, and exit code for a detached one-shot run.",
		Example: `  latere cella run status run_123
  latere cella run status run_123 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			out, err := oneShotRunStatus(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			if printJSONOut {
				return printJSON(out)
			}
			printDetachedOneShotRun(out)
			if out.ExitCode != nil {
				fmt.Fprintf(os.Stderr, "exit_code=%d\n", *out.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	cmd.Flags().BoolVar(&printJSONOut, "json", false, "print response as JSON")
	return cmd
}

func newCeRunLogsCmd() *cobra.Command {
	var (
		apiURL string
		cursor int64
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs <run_id>",
		Short: "Read or follow detached one-shot run logs.",
		Long: `Read logs from a detached one-shot run.

Use --cursor to resume from a byte offset printed by a previous logs
call. Use --follow to keep streaming until the run exits.`,
		Example: `  latere cella run logs run_123
  latere cella run logs run_123 --cursor 2048
  latere cella run logs run_123 --follow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if follow {
				return streamOneShotRunLogs(cmd.Context(), c, args[0], cursor)
			}
			out, err := fetchOneShotRunLogs(cmd.Context(), c, args[0], cursor)
			if err != nil {
				return err
			}
			fmt.Print(out.Bytes)
			fmt.Fprintf(os.Stderr, "[cursor=%d state=%s]\n", out.NextCursor, out.Phase)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.Int64Var(&cursor, "cursor", 0, "byte offset to start from")
	f.BoolVarP(&follow, "follow", "f", false, "stream until the run exits")
	return cmd
}

func newCeRunCancelCmd() *cobra.Command {
	var (
		apiURL       string
		printJSONOut bool
	)
	cmd := &cobra.Command{
		Use:   "cancel <run_id>",
		Short: "Cancel a detached one-shot run.",
		Long:  "Request cancellation for a detached one-shot run and print the updated run status.",
		Example: `  latere cella run cancel run_123
  latere cella run cancel run_123 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			out, err := oneShotRunCancel(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			if printJSONOut {
				return printJSON(out)
			}
			printDetachedOneShotRun(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	cmd.Flags().BoolVar(&printJSONOut, "json", false, "print response as JSON")
	return cmd
}

func newCeLogsCmd() *cobra.Command {
	var (
		apiURL string
		cursor int64
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs <name|id> <command_id>",
		Short: "Read or follow command logs.",
		Long: `Read logs from a command previously started in an existing cella.

Use the command id printed by 'latere cella run <name|id> -- <argv>...'.
Use --cursor to resume from a byte offset, or --follow to stream until
the command exits.`,
		Example: `  latere cella run dev -- make test
  latere cella logs dev cmd_123
  latere cella logs dev cmd_123 --cursor 4096
  latere cella logs dev cmd_123 --follow`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if follow {
				return streamLogs(cmd.Context(), c, args[0], args[1], cursor)
			}
			out, err := fetchLogsCursor(cmd.Context(), c, args[0], args[1], cursor)
			if err != nil {
				return err
			}
			fmt.Print(out.Bytes)
			fmt.Fprintf(os.Stderr, "[cursor=%d phase=%s]\n", out.NextCursor, out.Phase)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.Int64Var(&cursor, "cursor", 0, "byte offset to start from")
	f.BoolVarP(&follow, "follow", "f", false, "stream until command exits")
	return cmd
}

func newCeWaitCmd() *cobra.Command {
	var (
		apiURL string
		secs   int
	)
	cmd := &cobra.Command{
		Use:   "wait <name|id> <command_id>",
		Short: "Poll a command until it terminates or --timeout passes.",
		Long:  "Wait for a background command in an existing cella and exit with the remote exit code when available.",
		Example: `  latere cella run dev -- make test
  latere cella wait dev cmd_123
  latere cella wait dev cmd_123 --timeout 1200`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			cd, err := waitCommand(cmd.Context(), c, args[0], args[1], time.Duration(secs)*time.Second)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "phase=%s", cd.Phase)
			if cd.ExitCode != nil {
				fmt.Fprintf(os.Stderr, " exit_code=%d", *cd.ExitCode)
				os.Exit(*cd.ExitCode)
			}
			fmt.Fprintln(os.Stderr)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	cmd.Flags().IntVar(&secs, "timeout", 600, "max poll seconds")
	return cmd
}

// ---- import / export ----

func newCeExportCmd() *cobra.Command {
	var (
		apiURL string
		srcDir string
		out    string
	)
	cmd := &cobra.Command{
		Use:   "export <name|id> [paths...]",
		Short: "Stream a tar of files from the cella workspace.",
		Long: `Export files from a Cella workspace as a tar stream.

By default paths are resolved under /workspace and the tar is written
to stdout. Pass --output to write the archive to a local file.`,
		Example: `  latere cella export dev -o workspace.tar
  latere cella export dev src package.json -o app.tar
  latere cella export dev --src-dir /workspace/results logs -o results.tar`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if srcDir != "" {
				body["src_dir"] = srcDir
			}
			if len(args) > 1 {
				body["paths"] = args[1:]
			}
			path := sbPath(args[0]) + "/files/export"
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			resp, err := c.DoRaw(cmd.Context(), http.MethodPost, path,
				bytes.NewReader(b), "application/json")
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			var w io.Writer = os.Stdout
			if out != "" && out != "-" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				w = f
			}
			_, err = io.Copy(w, resp.Body)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.StringVar(&srcDir, "src-dir", "", "directory inside the cella; default /workspace")
	f.StringVarP(&out, "output", "o", "-", "output tar path (- for stdout)")
	return cmd
}

func newCeImportCmd() *cobra.Command {
	var (
		apiURL  string
		dest    string
		input   string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "import <name|id>",
		Short: "Upload files into the cella workspace (reads stdin or --input).",
		Long: `Import files into a Cella workspace.

Tar archives are extracted. Zip archives are converted to tar before
upload. A regular file is copied as a single file into the destination
directory.`,
		Example: `  latere cella import dev --input workspace.tar
  latere cella import dev --input app.zip --dest /workspace/app
  tar -cf - src package.json | latere cella import dev --dest /workspace/app`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if c.HTTP != nil {
				c.HTTP.Timeout = timeout
			}
			var (
				src          io.Reader = os.Stdin
				srcFile      *os.File
				formFilename = "import.tar"
				inputKind    = importInputTar
			)
			if input != "" && input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				src = f
				srcFile = f
				formFilename = filepath.Base(input)
				inputKind, err = classifyImportInput(input, f)
				if err != nil {
					return err
				}
			}
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			go func() {
				if dest != "" {
					if err := mw.WriteField("dest", dest); err != nil {
						_ = pw.CloseWithError(err)
						return
					}
				}
				fw, err := mw.CreateFormFile("tarball", formFilename)
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				switch inputKind {
				case importInputRegularFile:
					err = writeSingleFileTar(fw, input, srcFile)
				case importInputZip:
					err = writeZipAsTar(fw, input, srcFile)
				default:
					_, err = io.Copy(fw, src)
				}
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				if err := mw.Close(); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				_ = pw.Close()
			}()
			path := sbPath(args[0]) + "/files/import"
			var resp struct {
				Imported string `json:"imported"`
				Bytes    int64  `json:"bytes"`
				Dest     string `json:"dest"`
			}
			if err := c.Do(cmd.Context(), http.MethodPost, path, pr,
				mw.FormDataContentType(), &resp); err != nil {
				return err
			}
			return printJSON(resp)
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.StringVar(&dest, "dest", "", "destination dir in the cella; default /workspace")
	f.StringVarP(&input, "input", "i", "-", "input path; tar archives are extracted, regular files are copied")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "HTTP timeout covering upload and extraction (0 disables)")
	return cmd
}

type importInputKind int

const (
	importInputTar importInputKind = iota
	importInputRegularFile
	importInputZip
)

func classifyImportInput(name string, f *os.File) (importInputKind, error) {
	info, err := f.Stat()
	if err != nil {
		return importInputTar, err
	}
	if info.IsDir() {
		return importInputTar, fmt.Errorf("input must be a file, got directory: %s", name)
	}
	if hasZipExtension(name) {
		return importInputZip, nil
	}
	if hasTarExtension(name) {
		return importInputTar, nil
	}
	kind, err := sniffImportInput(f)
	if err != nil {
		return importInputTar, err
	}
	return kind, nil
}

func hasTarExtension(name string) bool {
	name = strings.ToLower(name)
	for _, suffix := range []string{".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz", ".tbz2", ".tar.xz", ".txz"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func hasZipExtension(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".zip")
}

func sniffImportInput(f *os.File) (importInputKind, error) {
	var block [512]byte
	n, err := io.ReadFull(f, block[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return importInputTar, err
	}
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return importInputTar, seekErr
	}
	if n >= 4 && string(block[:2]) == "PK" &&
		(block[2] == 0x03 || block[2] == 0x05 || block[2] == 0x07) &&
		(block[3] == 0x04 || block[3] == 0x06 || block[3] == 0x08) {
		return importInputZip, nil
	}
	if n < len(block) {
		return importInputRegularFile, nil
	}
	if string(block[257:262]) == "ustar" {
		return importInputTar, nil
	}
	return importInputRegularFile, nil
}

func writeSingleFileTar(dst io.Writer, name string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tw := tar.NewWriter(dst)
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(name)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		return err
	}
	return tw.Close()
}

func writeZipAsTar(dst io.Writer, name string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return fmt.Errorf("read zip %s: %w", name, err)
	}
	tw := tar.NewWriter(dst)
	for _, zf := range zr.File {
		if !safeArchivePath(zf.Name) {
			return fmt.Errorf("zip entry has unsafe path: %s", zf.Name)
		}
		hdr, err := tar.FileInfoHeader(zf.FileInfo(), "")
		if err != nil {
			return err
		}
		hdr.Name = strings.TrimPrefix(zf.Name, "./")
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return tw.Close()
}

// safeArchivePath accepts a zip entry name that names a file below the
// archive root as written: not empty, not absolute, no NUL, no ".." element,
// and nothing path.Clean would rewrite, because the name goes into the tar
// header verbatim.
func safeArchivePath(name string) bool {
	name = strings.TrimPrefix(name, "./")
	clean, err := relpath.Clean(name)
	return err == nil && clean == name && clean != "."
}

// ---- extend / convert ----

// newCeExtendCmd pushes the auto-delete deadline of an ephemeral
// cella forward. Persistent cellas have no deadline so the API 409s.
func newCeExtendCmd() *cobra.Command {
	var (
		apiURL   string
		hours    int
		deadline string
	)
	cmd := &cobra.Command{
		Use:   "extend <name|id>",
		Short: "Push the auto-delete deadline of an ephemeral cella forward.",
		Long: `Extend an ephemeral cella's auto-delete deadline.

Persistent cellas do not have an auto-delete deadline, so this command
only applies to ephemeral cellas.`,
		Example: `  latere cella extend dev
  latere cella extend dev --hours 72
  latere cella extend dev --deadline 2026-05-05T18:00:00Z`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if deadline != "" {
				t, err := time.Parse(time.RFC3339, deadline)
				if err != nil {
					return fmt.Errorf("--deadline must be RFC3339: %w", err)
				}
				body["deadline"] = t
			} else {
				if hours <= 0 {
					hours = 24
				}
				body["auto_delete_hours"] = hours
			}
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			path := sbPath(args[0]) + "/extend"
			if err := c.Do(cmd.Context(), http.MethodPost, path,
				bytes.NewReader(b), "application/json", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.IntVar(&hours, "hours", 24, "push deadline to now + N hours")
	f.StringVar(&deadline, "deadline", "", "absolute RFC3339 deadline (overrides --hours)")
	return cmd
}

// newCeConvertCmd flips a cella between ephemeral and persistent.
// Persistent → ephemeral requires --hours so the new lifetime is
// explicit; the API rejects the request otherwise.
func newCeConvertCmd() *cobra.Command {
	var (
		apiURL string
		to     string
		hours  int
	)
	cmd := &cobra.Command{
		Use:   "convert <name|id> --to {ephemeral|persistent}",
		Short: "Switch a cella between ephemeral and persistent.",
		Long: `Convert a cella between ephemeral and persistent tiers.

Converting to persistent removes the auto-delete deadline. Converting
to ephemeral requires --hours so the new auto-delete deadline is
explicit.`,
		Example: `  latere cella convert dev --to persistent
  latere cella convert dev --to ephemeral --hours 48`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to != "ephemeral" && to != "persistent" {
				return fmt.Errorf("--to must be ephemeral or persistent")
			}
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			body := map[string]any{"tier": to}
			if to == "ephemeral" {
				if hours <= 0 {
					return fmt.Errorf("--hours is required when converting to ephemeral")
				}
				body["auto_delete_hours"] = hours
			}
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			path := sbPath(args[0]) + "/convert"
			if err := c.Do(cmd.Context(), http.MethodPost, path,
				bytes.NewReader(b), "application/json", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.StringVar(&to, "to", "", "destination tier: ephemeral or persistent")
	f.IntVar(&hours, "hours", 0, "auto-delete-hours; required when --to=ephemeral")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// newCeResizeCmd grows a persistent cella's workspace disk. Disk can only
// grow, so the API rejects a size at or below the current one; ephemeral
// cellas are rejected since they are short-lived.
func newCeResizeCmd() *cobra.Command {
	var (
		apiURL string
		diskGB int
	)
	cmd := &cobra.Command{
		Use:   "resize <name|id> --disk-gb N",
		Short: "Grow a persistent cella's workspace disk.",
		Long: `Grow a persistent cella's workspace disk to N GiB.

Disk can only grow: a size at or below the current one is rejected.
Only persistent cellas can be resized.`,
		Example: `  latere cella resize dev --disk-gb 50`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if diskGB <= 0 {
				return fmt.Errorf("--disk-gb must be a positive size")
			}
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			b, err := json.Marshal(map[string]any{"disk_gb": diskGB})
			if err != nil {
				return err
			}
			var sb sandboxDTO
			path := sbPath(args[0]) + "/resize"
			if err := c.Do(cmd.Context(), http.MethodPost, path,
				bytes.NewReader(b), "application/json", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.IntVar(&diskGB, "disk-gb", 0, "new workspace size in GiB; must exceed the current size")
	_ = cmd.MarkFlagRequired("disk-gb")
	return cmd
}

// ---- granular file ops ----

// newCeCatCmd streams a single file from the cella to stdout.
func newCeCatCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "cat <name|id> <path>",
		Short:   "Stream a file from the cella to stdout.",
		Example: `  latere cella cat dev /workspace/out.log`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			path := sbPath(args[0]) + "/files?path=" + url.QueryEscape(args[1]) + "&raw=true"
			resp, err := c.DoRaw(cmd.Context(), http.MethodGet, path, nil, "")
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			_, err = io.Copy(os.Stdout, resp.Body)
			return err
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

// newCeWriteCmd writes a single file into the cella from --input or stdin.
func newCeWriteCmd() *cobra.Command {
	var (
		apiURL string
		input  string
	)
	cmd := &cobra.Command{
		Use:   "write <name|id> <path>",
		Short: "Write a file into the cella (reads stdin or --input).",
		Example: `  echo hi | latere cella write dev /workspace/note.txt
  latere cella write dev /workspace/app.tar -f app.tar`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var src io.Reader = os.Stdin
			if input != "" && input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				src = f
			}
			content, err := io.ReadAll(src)
			if err != nil {
				return err
			}
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			b, err := json.Marshal(map[string]any{
				"path":    args[1],
				"content": base64.StdEncoding.EncodeToString(content),
			})
			if err != nil {
				return err
			}
			return c.Do(cmd.Context(), http.MethodPut, sbPath(args[0])+"/files",
				bytes.NewReader(b), "application/json", nil)
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.StringVarP(&input, "input", "f", "", "read content from this file (- or empty for stdin)")
	return cmd
}

// newCeLsCmd lists a directory inside the cella.
func newCeLsCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "ls <name|id> <path>",
		Short:   "List a directory inside the cella.",
		Example: `  latere cella ls dev /workspace`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			path := sbPath(args[0]) + "/files?path=" + url.QueryEscape(args[1]) + "&list=true"
			var resp struct {
				Entries []struct {
					Name  string `json:"name"`
					Size  int64  `json:"size"`
					Mode  uint32 `json:"mode"`
					IsDir bool   `json:"is_directory"`
				} `json:"entries"`
			}
			if err := c.Do(cmd.Context(), http.MethodGet, path, nil, "", &resp); err != nil {
				return err
			}
			for _, e := range resp.Entries {
				name := e.Name
				if e.IsDir {
					name += "/"
				}
				fmt.Printf("%04o\t%d\t%s\n", e.Mode, e.Size, name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

// newCeUploadCmd streams files and folders into the cella, preserving folder
// structure. Each file is sent as a multipart part whose form-field name is its
// path relative to the destination.
func newCeUploadCmd() *cobra.Command {
	var (
		apiURL  string
		dest    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "upload <name|id> <src...> --dest D",
		Short: "Stream files/folders into the cella (folder-preserving).",
		Example: `  latere cella upload dev ./dist --dest /workspace
  latere cella upload dev a.txt b.txt --dest /tmp`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if c.HTTP != nil && timeout > 0 {
				c.HTTP.Timeout = timeout
			}
			type upfile struct{ rel, local string }
			var files []upfile
			for _, src := range args[1:] {
				info, err := os.Stat(src)
				if err != nil {
					return err
				}
				if info.IsDir() {
					parent := filepath.Dir(filepath.Clean(src))
					if werr := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if d.IsDir() {
							return nil
						}
						rel, err := filepath.Rel(parent, p)
						if err != nil {
							return err
						}
						files = append(files, upfile{rel: filepath.ToSlash(rel), local: p})
						return nil
					}); werr != nil {
						return werr
					}
					continue
				}
				files = append(files, upfile{rel: filepath.Base(src), local: src})
			}
			if len(files) == 0 {
				return fmt.Errorf("no files to upload")
			}
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			contentType := mw.FormDataContentType()
			go func() {
				if dest != "" {
					if err := mw.WriteField("dest", dest); err != nil {
						_ = pw.CloseWithError(err)
						return
					}
				}
				for _, uf := range files {
					f, err := os.Open(uf.local)
					if err != nil {
						_ = pw.CloseWithError(err)
						return
					}
					part, err := mw.CreateFormFile(uf.rel, filepath.Base(uf.local))
					if err != nil {
						_ = f.Close()
						_ = pw.CloseWithError(err)
						return
					}
					if _, err := io.Copy(part, f); err != nil {
						_ = f.Close()
						_ = pw.CloseWithError(err)
						return
					}
					_ = f.Close()
				}
				_ = pw.CloseWithError(mw.Close())
			}()
			var resp struct {
				Dest  string `json:"dest"`
				Files int    `json:"files"`
				Bytes int64  `json:"bytes"`
			}
			if err := c.Do(cmd.Context(), http.MethodPost, sbPath(args[0])+"/files/upload",
				pr, contentType, &resp); err != nil {
				return err
			}
			fmt.Printf("uploaded %d files (%d bytes) to %s\n", resp.Files, resp.Bytes, resp.Dest)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	f.StringVar(&dest, "dest", "", "destination directory inside the cella; default /workspace")
	f.DurationVar(&timeout, "timeout", 5*time.Minute, "upload timeout")
	return cmd
}

// newCeMkdirCmd creates a directory inside the cella.
func newCeMkdirCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "mkdir <name|id> <path>",
		Short:   "Create a directory inside the cella.",
		Example: `  latere cella mkdir dev /workspace/build`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			b, err := json.Marshal(map[string]any{"path": args[1]})
			if err != nil {
				return err
			}
			return c.Do(cmd.Context(), http.MethodPost, sbPath(args[0])+"/files/mkdir",
				bytes.NewReader(b), "application/json", nil)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

// newCeRmCmd deletes a file or directory tree inside the cella.
func newCeRmCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "rm <name|id> <path>",
		Short:   "Delete a file or directory (recursive) inside the cella.",
		Example: `  latere cella rm dev /workspace/old`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			path := sbPath(args[0]) + "/files?path=" + url.QueryEscape(args[1])
			return c.Do(cmd.Context(), http.MethodDelete, path, nil, "", nil)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

// newCeMvCmd renames or moves a file or directory inside the cella.
func newCeMvCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "mv <name|id> <from> <to>",
		Short:   "Rename or move a file or directory inside the cella.",
		Example: `  latere cella mv dev /workspace/a.txt /workspace/b.txt`,
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			b, err := json.Marshal(map[string]any{"from": args[1], "to": args[2]})
			if err != nil {
				return err
			}
			return c.Do(cmd.Context(), http.MethodPost, sbPath(args[0])+"/files/move",
				bytes.NewReader(b), "application/json", nil)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

// ---- helpers (HTTP composition + UI) ----

func authedClient(apiURL string) (*api.Client, error) {
	c := api.NewClient(apiURL)
	if err := c.MustRequireAuth(); err != nil {
		return nil, err
	}
	return c, nil
}

func sbPath(idOrName string) string {
	return "/v1/sandboxes/" + url.PathEscape(idOrName)
}

func runPath(runID string) string {
	return "/v1/one-shot-runs/" + url.PathEscape(runID)
}

func startCommand(ctx context.Context, c *api.Client, sandbox string, argv []string, env map[string]string, cwd string, credentialCatalog []string) (commandDTO, error) {
	body := map[string]any{
		"argv":   argv,
		"detach": true,
	}
	if len(env) > 0 {
		body["env"] = env
	}
	if cwd != "" {
		body["cwd"] = cwd
	}
	if len(credentialCatalog) > 0 {
		body["credential_catalog"] = credentialCatalog
	}
	var cd commandDTO
	err := c.PostJSON(ctx, sbPath(sandbox)+"/commands", body, &cd)
	return cd, err
}

func waitCommand(ctx context.Context, c *api.Client, sandbox, cmdID string, timeout time.Duration) (commandDTO, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		var cd commandDTO
		path := sbPath(sandbox) + "/commands/" + url.PathEscape(cmdID)
		if err := c.GetJSON(ctx, path, &cd); err != nil {
			return cd, err
		}
		if cd.Phase != "running" {
			return cd, nil
		}
		if time.Now().After(deadline) {
			return cd, errors.New("wait timed out")
		}
		select {
		case <-ctx.Done():
			return cd, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func fetchLogsCursor(ctx context.Context, c *api.Client, sandbox, cmdID string, cursor int64) (logsCursorDTO, error) {
	q := url.Values{}
	q.Set("cursor", strconv.FormatInt(cursor, 10))
	q.Set("stream", "false")
	path := sbPath(sandbox) + "/commands/" + url.PathEscape(cmdID) + "/logs?" + q.Encode()
	var out logsCursorDTO
	err := c.GetJSON(ctx, path, &out)
	return out, err
}

// streamLogs polls cursor-based logs until the command terminates.
// SSE follow mode is the alternative; cursor polling works against
// a simpler sandboxd build and survives reconnects naturally.
func streamLogs(ctx context.Context, c *api.Client, sandbox, cmdID string, cursor int64) error {
	for {
		out, err := fetchLogsCursor(ctx, c, sandbox, cmdID, cursor)
		if err != nil {
			return err
		}
		if out.Bytes != "" {
			fmt.Print(out.Bytes)
		}
		cursor = out.NextCursor
		if out.Phase != "running" {
			if out.ExitCode != nil {
				os.Exit(*out.ExitCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func streamOneShotRunLogs(ctx context.Context, c *api.Client, runID string, cursor int64) error {
	for {
		out, err := fetchOneShotRunLogs(ctx, c, runID, cursor)
		if err != nil {
			return err
		}
		if out.Bytes != "" {
			fmt.Print(out.Bytes)
		}
		cursor = out.NextCursor
		if out.Phase != "creating" && out.Phase != "running" {
			if out.ExitCode != nil {
				os.Exit(*out.ExitCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// runAndStream is the foreground equivalent: start a detached command
// then tail its logs until exit. Used by `latere exec` and
// `latere sandbox run --follow`.
func runAndStream(ctx context.Context, c *api.Client, sandbox string, argv []string, env map[string]string, cwd string, credentialCatalog []string) error {
	cd, err := startCommand(ctx, c, sandbox, argv, env, cwd, credentialCatalog)
	if err != nil {
		return err
	}
	return streamLogs(ctx, c, sandbox, cd.CommandID, 0)
}

func oneShotRunBody(argv []string, env map[string]string, cwd, image string, diskGB int, cpu, memory string, timeout int, credentialCatalog []string) map[string]any {
	body := map[string]any{"argv": argv}
	if len(env) > 0 {
		body["env"] = env
	}
	if cwd != "" {
		body["cwd"] = cwd
	}
	if image != "" {
		body["image"] = image
	}
	if diskGB > 0 {
		body["disk_gb"] = diskGB
	}
	if cpu != "" {
		body["cpu"] = cpu
	}
	if memory != "" {
		body["memory"] = memory
	}
	if timeout > 0 {
		body["timeout_seconds"] = timeout
	}
	if len(credentialCatalog) > 0 {
		body["credential_catalog"] = credentialCatalog
	}
	return body
}

func oneShotRun(ctx context.Context, c *api.Client, argv []string, env map[string]string, cwd, image string, diskGB int, cpu, memory string, timeout int, credentialCatalog []string) (oneShotRunDTO, error) {
	body := oneShotRunBody(argv, env, cwd, image, diskGB, cpu, memory, timeout, credentialCatalog)
	if c.HTTP != nil {
		effective := timeout
		if effective <= 0 {
			effective = 600
		}
		c.HTTP.Timeout = time.Duration(effective+180) * time.Second
	}
	var out oneShotRunDTO
	err := c.PostJSON(ctx, "/v1/one-shot-runs", body, &out)
	return out, err
}

func oneShotRunDetached(ctx context.Context, c *api.Client, argv []string, env map[string]string, cwd, image string, diskGB int, cpu, memory string, timeout int, credentialCatalog []string) (oneShotRunDTO, error) {
	body := oneShotRunBody(argv, env, cwd, image, diskGB, cpu, memory, timeout, credentialCatalog)
	var out oneShotRunDTO
	err := c.PostJSON(ctx, "/v1/one-shot-runs?detach=true", body, &out)
	return out, err
}

func oneShotRunStatus(ctx context.Context, c *api.Client, runID string) (oneShotRunDTO, error) {
	var out oneShotRunDTO
	err := c.GetJSON(ctx, runPath(runID), &out)
	return out, err
}

func oneShotRunCancel(ctx context.Context, c *api.Client, runID string) (oneShotRunDTO, error) {
	var out oneShotRunDTO
	err := c.Do(ctx, http.MethodDelete, runPath(runID), nil, "", &out)
	return out, err
}

func fetchOneShotRunLogs(ctx context.Context, c *api.Client, runID string, cursor int64) (logsCursorDTO, error) {
	q := url.Values{}
	q.Set("cursor", strconv.FormatInt(cursor, 10))
	q.Set("stream", "false")
	path := runPath(runID) + "/logs?" + q.Encode()
	var out logsCursorDTO
	err := c.GetJSON(ctx, path, &out)
	return out, err
}

func printDetachedOneShotRun(out oneShotRunDTO) {
	fmt.Fprintf(os.Stderr, "run_id=%s state=%s", out.RunID, out.State)
	if out.SandboxName != "" {
		fmt.Fprintf(os.Stderr, " sandbox=%s", out.SandboxName)
	}
	if out.Timing.TotalMS > 0 {
		fmt.Fprintf(os.Stderr, " total=%s", humanDurationMS(out.Timing.TotalMS))
	}
	fmt.Fprintln(os.Stderr)
	if out.Links != nil {
		if logs := out.Links["logs"]; logs != "" {
			fmt.Fprintf(os.Stderr, "logs=%s\n", logs)
		}
	}
}

func printStartedDetachedOneShotRun(out oneShotRunDTO) {
	fmt.Println(out.RunID)
	printDetachedOneShotRun(out)
}

func printOneShotRun(out oneShotRunDTO) {
	if out.Stdout != "" {
		fmt.Print(out.Stdout)
	}
	if out.Stderr != "" {
		fmt.Fprint(os.Stderr, out.Stderr)
	}
	fmt.Fprintf(os.Stderr, "✓ cella created  %s  ·  %s\n", out.SandboxName, humanDurationMS(out.Timing.CreateMS))
	if out.ExitCode != nil {
		fmt.Fprintf(os.Stderr, "✓ command exited %d  ·  %s\n", *out.ExitCode, humanDurationMS(out.Timing.ExecMS))
	} else {
		fmt.Fprintf(os.Stderr, "✓ command %s  ·  %s\n", out.State, humanDurationMS(out.Timing.ExecMS))
	}
	if out.CleanupError != "" {
		fmt.Fprintf(os.Stderr, "✗ sandbox cleanup failed  ·  %s\n", out.CleanupError)
	} else {
		fmt.Fprintf(os.Stderr, "✓ cella deleted  ·  total %s\n", humanDurationMS(out.Timing.TotalMS))
	}
	if out.Truncated {
		fmt.Fprintln(os.Stderr, "output truncated")
	}
	if out.Error != "" {
		fmt.Fprintf(os.Stderr, "%s\n", out.Error)
	}
}

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

func jsonReader(v any) (io.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printSandboxList(sbs []sandboxDTO) {
	for i, s := range sbs {
		if i > 0 {
			fprintln(os.Stdout)
		}
		printSandbox(s)
	}
}

func printSandbox(s sandboxDTO) {
	printWrappedField("cella", nameOrDash(s.Name))
	printWrappedField("id", s.ID)
	printWrappedField("state", s.State)
	printWrappedField("tier", defaultStr(s.Tier, "-"))
	if s.DiskGB > 0 {
		printWrappedField("disk", fmt.Sprintf("%dGi", s.DiskGB))
	}
	if size := sandboxResourceSummary(s); size != "" {
		printWrappedField("resources", size)
	}
	if !s.CreatedAt.IsZero() {
		printWrappedField("created", humanAge(s.CreatedAt)+" ago")
	}
	if !s.Deadline.IsZero() {
		printWrappedField("deadline", s.Deadline.Format(time.RFC3339))
	}
	if s.Workdir != "" {
		printWrappedField("workdir", s.Workdir)
	}
}

// sandboxResourceSummary renders the cpu_milli / memory_mb fields
// the server populates from the canonical annotations. Empty when
// neither has been reported yet (older sandboxd build or sandbox
// still creating).
func sandboxResourceSummary(s sandboxDTO) string {
	switch {
	case s.CPUMilli > 0 && s.MemoryMB > 0:
		return fmt.Sprintf("cpu=%dm memory=%dMi", s.CPUMilli, s.MemoryMB)
	case s.CPUMilli > 0:
		return fmt.Sprintf("cpu=%dm", s.CPUMilli)
	case s.MemoryMB > 0:
		return fmt.Sprintf("memory=%dMi", s.MemoryMB)
	}
	return ""
}

func nameOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func printWrappedField(label, value string) {
	value = oneLine(value)
	if value == "" {
		return
	}
	const (
		labelWidth = 12
		maxWidth   = 88
	)
	prefix := fmt.Sprintf("%-*s", labelWidth, label+":")
	lines := wrapText(value, maxWidth-labelWidth)
	if len(lines) == 0 {
		fprintln(os.Stdout, prefix)
		return
	}
	fprintln(os.Stdout, prefix+lines[0])
	indent := strings.Repeat(" ", labelWidth)
	for _, line := range lines[1:] {
		fprintln(os.Stdout, indent+line)
	}
}

func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	if width <= 0 {
		width = 76
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	lines = append(lines, line)
	return lines
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func humanDurationMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	if ms < 10_000 {
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%d s", ms/1000)
}
