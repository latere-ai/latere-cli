package commands

import (
	"archive/tar"
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
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

// newCeMcpCmd builds `latere cella mcp` — a stdio MCP server that
// exposes the cella API as MCP tools. It reuses internal/api so the
// same login covers the CLI and the MCP host.
//
// Wire into Claude Code with:
//
//	"latere-cella": { "command": "latere", "args": ["cella", "mcp"] }
//
// Reference: https://modelcontextprotocol.io.
func newCeMcpCmd() *cobra.Command {
	var (
		apiURL    string
		surface   string
		sandboxes []string
		autoStart bool
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP stdio server for agent access to Cella sandboxes.",
		Long: `Run a Model Context Protocol server over stdio.

By default this exposes a focused multi-sandbox agent surface:
Sandboxes, Read, Write, Edit, Bash, Monitor, Glob, Grep, Upload,
and Download. Every action tool takes a sandbox selector, which can
be an alias configured with --sandbox, a sandbox id, or a slug.

The legacy lifecycle-heavy tool surface is still available with
--surface=management.

Env fields are literal non-secret environment variables. Credentials should
come from the Cella trust-plane catalog configured for the selected sandbox.

The token at ~/.config/latere/token.json (written by 'latere auth login')
is used for every call. A missing token starts the server anyway so the
auth failure surfaces on first tool use rather than at boot.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := api.NewClient(apiURL)
			aliases, err := parseSandboxAliases(sandboxes)
			if err != nil {
				return err
			}
			return runMCPServer(cmd.Context(), c, mcpServerConfig{
				Surface:   surface,
				Aliases:   aliases,
				AutoStart: autoStart,
			})
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override cella base URL")
	cmd.Flags().StringVar(&surface, "surface", "agent", "MCP tool surface: agent, management, or all")
	cmd.Flags().StringArrayVar(&sandboxes, "sandbox", nil, "sandbox alias mapping alias=id-or-slug; repeatable")
	cmd.Flags().BoolVar(&autoStart, "auto-start", true, "auto-start stopped sandboxes selected by agent tools")
	return cmd
}

type mcpServerConfig struct {
	Surface   string
	Aliases   map[string]string
	AutoStart bool
}

// runMCPServer wires the MCP tools onto a stdio server and runs until
// the host disconnects (or ctx is cancelled).
func runMCPServer(ctx context.Context, c *api.Client, cfg mcpServerConfig) error {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "latere-cella",
		Version: "0.3.0",
	}, nil)

	if cfg.Surface == "" {
		cfg.Surface = "agent"
	}
	if cfg.Surface != "agent" && cfg.Surface != "management" && cfg.Surface != "all" {
		return fmt.Errorf("surface must be agent, management, or all")
	}
	mt := &mcpTools{c: c, aliases: cfg.Aliases, autoStart: cfg.AutoStart}
	if cfg.Surface == "agent" || cfg.Surface == "all" {
		registerAgentTools(srv, mt)
	}
	if cfg.Surface == "management" || cfg.Surface == "all" {
		registerManagementTools(srv, mt)
	}

	t := &mcp.LoggingTransport{Transport: &mcp.StdioTransport{}, Writer: os.Stderr}
	return srv.Run(ctx, t)
}

func registerAgentTools(srv *mcp.Server, mt *mcpTools) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Sandboxes",
		Description: "List Cella sandboxes available to this MCP server, including aliases, ids, state, tier, and workdir.",
	}, mt.agentSandboxes)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Read",
		Description: "Read a text file from a selected Cella sandbox. Requires sandbox and path.",
	}, mt.agentRead)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Write",
		Description: "Create or replace a text file in a selected Cella sandbox. Requires sandbox, path, and content.",
	}, mt.agentWrite)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Edit",
		Description: "Make an exact string replacement in a text file in a selected Cella sandbox.",
	}, mt.agentEdit)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Bash",
		Description: "Run a shell command inside a selected Cella sandbox, never on the MCP host. Returns a single combined `output` field with stdout and stderr merged in emission order. Use credential_catalog for trust-plane credentials; env is only for non-secret values.",
	}, mt.agentBash)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Monitor",
		Description: "Read new output from a background command in a selected Cella sandbox. Events use a `combined` stream label because the runtime merges stdout and stderr.",
	}, mt.agentMonitor)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Glob",
		Description: "Find files by glob pattern inside a selected Cella sandbox.",
	}, mt.agentGlob)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Grep",
		Description: "Search file contents with a regex inside a selected Cella sandbox.",
	}, mt.agentGrep)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Upload",
		Description: "Upload a base64-encoded tar archive into a selected Cella sandbox.",
	}, mt.agentUpload)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "Download",
		Description: "Download files from a selected Cella sandbox as a base64-encoded tar archive.",
	}, mt.agentDownload)
}

func registerManagementTools(srv *mcp.Server, mt *mcpTools) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "CreateSandbox",
		Description: "Create a new Cella sandbox. Returns its id and slug. Attach credentials by catalog key, not by secret value. cpu and memory accept Kubernetes quantities, for example 1.5, 1500m, or 4Gi.",
	}, mt.create)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ListSandboxes",
		Description: "List the caller's Cella sandboxes with state, tier, and disk.",
	}, mt.list)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "GetSandbox",
		Description: "Fetch one Cella sandbox by id or slug.",
	}, mt.get)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "StartSandbox",
		Description: "Start a stopped Cella sandbox.",
	}, mt.start)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "StopSandbox",
		Description: "Stop a running Cella sandbox; the disk persists if tier is persistent.",
	}, mt.stop)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ExtendSandbox",
		Description: "Push the auto-delete deadline of an ephemeral cella forward by auto_delete_hours (default 24).",
	}, mt.extend)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ConvertSandbox",
		Description: "Switch a cella between ephemeral and persistent. Persistent → ephemeral requires auto_delete_hours.",
	}, mt.convert)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "RunCommand",
		Description: "Start a command in the background; returns command_id immediately. Use credential_catalog for trust-plane credentials; env is only for non-secret values.",
	}, mt.run)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "WaitCommand",
		Description: "Poll a command until it terminates or timeout_seconds passes.",
	}, mt.wait)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "CommandLogs",
		Description: "Read command logs starting at since_cursor. Returns bytes + next_cursor.",
	}, mt.logs)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "KillCommand",
		Description: "Kill a running command (sends DELETE on the command resource).",
	}, mt.kill)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ExportFiles",
		Description: "Tar files from the selected sandbox; returns base64-encoded tar.",
	}, mt.export)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ImportFiles",
		Description: "Upload a base64-encoded tar into the selected sandbox.",
	}, mt.imp)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "DeleteSandbox",
		Description: "Delete a cella.",
	}, mt.del)
}

// ---- tool args / results ----

type mcpCreateArgs struct {
	Image             string   `json:"image" mcp:"container image, e.g. ghcr.io/latere-ai/sandbox-base:main"`
	Tier              string   `json:"tier,omitempty" mcp:"ephemeral (default) or persistent"`
	DiskGB            int      `json:"disk_gb,omitempty"`
	CPU               string   `json:"cpu,omitempty" mcp:"CPU limit as a Kubernetes quantity, e.g. 1.5 or 1500m"`
	Memory            string   `json:"memory,omitempty" mcp:"memory limit as a Kubernetes quantity, e.g. 4Gi or 2048Mi"`
	Name              string   `json:"name,omitempty" mcp:"optional human slug"`
	AutoDeleteHours   int      `json:"auto_delete_hours,omitempty"`
	CredentialCatalog []string `json:"credential_catalog,omitempty" mcp:"trust-plane catalog keys to attach; not secret values"`
}
type mcpCreateResult struct {
	ID    string `json:"id"`
	Slug  string `json:"slug,omitempty"`
	State string `json:"state"`
}

type mcpRunArgs struct {
	Sandbox           string            `json:"sandbox" mcp:"sandbox id or slug"`
	Argv              []string          `json:"argv"`
	Env               map[string]string `json:"env,omitempty" mcp:"literal non-secret environment variables"`
	Cwd               string            `json:"cwd,omitempty"`
	CredentialCatalog []string          `json:"credential_catalog,omitempty" mcp:"trust-plane catalog keys to use for this command; not secret values"`
}
type mcpRunResult struct {
	CommandID string `json:"command_id"`
	Phase     string `json:"phase"`
}

type mcpWaitArgs struct {
	Sandbox        string `json:"sandbox"`
	CommandID      string `json:"command_id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" mcp:"max poll seconds; 0 = 60"`
}
type mcpWaitResult struct {
	Phase    string `json:"phase"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

type mcpLogsArgs struct {
	Sandbox     string `json:"sandbox"`
	CommandID   string `json:"command_id"`
	SinceCursor int64  `json:"since_cursor,omitempty"`
}
type mcpLogsResult struct {
	Bytes      string `json:"bytes"`
	NextCursor int64  `json:"next_cursor"`
	Phase      string `json:"phase"`
	ExitCode   *int   `json:"exit_code,omitempty"`
}

type mcpExportArgs struct {
	Sandbox string   `json:"sandbox"`
	SrcDir  string   `json:"src_dir,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}
type mcpExportResult struct {
	TarBase64 string `json:"tar_base64"`
	Bytes     int    `json:"bytes"`
}

type mcpImportArgs struct {
	Sandbox   string `json:"sandbox"`
	Dest      string `json:"dest,omitempty"`
	TarBase64 string `json:"tar_base64"`
}
type mcpImportResult struct {
	Imported string `json:"imported"`
	Bytes    int64  `json:"bytes"`
	Dest     string `json:"dest"`
}

type mcpDeleteArgs struct {
	Sandbox string `json:"sandbox"`
}
type mcpDeleteResult struct {
	OK bool `json:"ok"`
}

type mcpAgentSandboxesArgs struct {
	IncludeStopped bool   `json:"include_stopped,omitempty"`
	NameFilter     string `json:"name,omitempty" mcp:"optional name filter"`
}
type mcpAgentSandbox struct {
	Alias   string `json:"alias,omitempty"`
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	State   string `json:"state"`
	Tier    string `json:"tier,omitempty"`
	DiskGB  int    `json:"disk_gb,omitempty"`
	Workdir string `json:"workdir,omitempty"`
}
type mcpAgentSandboxesResult struct {
	Sandboxes []mcpAgentSandbox `json:"sandboxes"`
}

type mcpReadArgs struct {
	Sandbox string `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Path    string `json:"path" mcp:"file path relative to the sandbox workdir"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
type mcpReadResult struct {
	Sandbox   string `json:"sandbox"`
	Path      string `json:"path"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

type mcpWriteArgs struct {
	Sandbox    string `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Path       string `json:"path" mcp:"file path relative to the sandbox workdir"`
	Content    string `json:"content"`
	Mode       string `json:"mode,omitempty" mcp:"octal file mode, default 0644"`
	CreateOnly bool   `json:"create_only,omitempty"`
}
type mcpWriteResult struct {
	Sandbox string `json:"sandbox"`
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
}

type mcpEditArgs struct {
	Sandbox    string `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Path       string `json:"path" mcp:"file path relative to the sandbox workdir"`
	Old        string `json:"old"`
	New        string `json:"new"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}
type mcpEditResult struct {
	Sandbox      string `json:"sandbox"`
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

type mcpBashArgs struct {
	Sandbox           string            `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Command           string            `json:"command" mcp:"shell command to run inside the sandbox"`
	Cwd               string            `json:"cwd,omitempty" mcp:"working directory relative to the sandbox workdir"`
	TimeoutSeconds    int               `json:"timeout_seconds,omitempty"`
	Env               map[string]string `json:"env,omitempty" mcp:"literal non-secret environment variables"`
	CredentialCatalog []string          `json:"credential_catalog,omitempty" mcp:"trust-plane catalog keys to use for this command; not secret values"`
	Background        bool              `json:"background,omitempty"`
	MaxOutputBytes    int               `json:"max_output_bytes,omitempty"`
}
type mcpBashResult struct {
	Sandbox    string `json:"sandbox"`
	CommandID  string `json:"command_id"`
	Phase      string `json:"phase"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Output     string `json:"output,omitempty"`
	NextCursor int64  `json:"next_cursor,omitempty"`
	Truncated  bool   `json:"truncated"`
	TimedOut   bool   `json:"timed_out"`
}

type mcpMonitorArgs struct {
	Sandbox        string `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	CommandID      string `json:"command_id"`
	Cursor         int64  `json:"cursor,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxLines       int    `json:"max_lines,omitempty"`
}
type mcpMonitorEvent struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}
type mcpMonitorResult struct {
	Sandbox   string            `json:"sandbox"`
	CommandID string            `json:"command_id"`
	Phase     string            `json:"phase"`
	ExitCode  *int              `json:"exit_code,omitempty"`
	Cursor    int64             `json:"cursor"`
	Events    []mcpMonitorEvent `json:"events"`
	Truncated bool              `json:"truncated"`
	TimedOut  bool              `json:"timed_out"`
}

type mcpGlobArgs struct {
	Sandbox string `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
type mcpGlobResult struct {
	Sandbox   string   `json:"sandbox"`
	Matches   []string `json:"matches"`
	Truncated bool     `json:"truncated"`
}

type mcpGrepArgs struct {
	Sandbox    string `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}
type mcpGrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type mcpGrepResult struct {
	Sandbox   string         `json:"sandbox"`
	Matches   []mcpGrepMatch `json:"matches"`
	Truncated bool           `json:"truncated"`
}

type mcpUploadArgs struct {
	Sandbox   string `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Dest      string `json:"dest,omitempty"`
	TarBase64 string `json:"tar_base64"`
	Overwrite bool   `json:"overwrite,omitempty"`
}
type mcpUploadResult struct {
	Sandbox string `json:"sandbox"`
	Dest    string `json:"dest"`
	Bytes   int64  `json:"bytes"`
}

type mcpDownloadArgs struct {
	Sandbox string   `json:"sandbox" mcp:"sandbox alias, id, or slug"`
	Paths   []string `json:"paths,omitempty"`
	SrcDir  string   `json:"src_dir,omitempty"`
	Format  string   `json:"format,omitempty"`
}
type mcpDownloadResult struct {
	Sandbox   string `json:"sandbox"`
	TarBase64 string `json:"tar_base64"`
	Bytes     int    `json:"bytes"`
}

// ---- tool handlers ----

type mcpTools struct {
	c         *api.Client
	aliases   map[string]string
	autoStart bool
}

func (m *mcpTools) agentSandboxes(ctx context.Context, _ *mcp.CallToolRequest, a mcpAgentSandboxesArgs) (*mcp.CallToolResult, mcpAgentSandboxesResult, error) {
	q := url.Values{}
	if a.NameFilter != "" {
		q.Set("name", a.NameFilter)
	}
	p := "/v1/sandboxes"
	if e := q.Encode(); e != "" {
		p += "?" + e
	}
	var rows []struct {
		ID      string `json:"id"`
		Name    string `json:"name,omitempty"`
		State   string `json:"state"`
		Tier    string `json:"tier,omitempty"`
		DiskGB  int    `json:"disk_gb,omitempty"`
		Workdir string `json:"workdir,omitempty"`
	}
	if err := m.c.GetJSON(ctx, p, &rows); err != nil {
		return nil, mcpAgentSandboxesResult{}, err
	}
	out := mcpAgentSandboxesResult{}
	for _, row := range rows {
		if !a.IncludeStopped && row.State == "stopped" {
			continue
		}
		wd := row.Workdir
		if wd == "" {
			wd = fallbackWorkdir
		}
		out.Sandboxes = append(out.Sandboxes, mcpAgentSandbox{
			Alias:   m.aliasFor(row.ID, row.Name),
			ID:      row.ID,
			Name:    row.Name,
			State:   row.State,
			Tier:    row.Tier,
			DiskGB:  row.DiskGB,
			Workdir: wd,
		})
	}
	return mcpText("%d sandboxes", len(out.Sandboxes)), out, nil
}

func (m *mcpTools) agentRead(ctx context.Context, _ *mcp.CallToolRequest, a mcpReadArgs) (*mcp.CallToolResult, mcpReadResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpReadResult{}, err
	}
	rel, err := safeToolPath(a.Path)
	if err != nil {
		return nil, mcpReadResult{}, err
	}
	abs := absUnderWorkdir(workdir, rel)
	content, total, truncated, err := m.readTextFile(ctx, sandbox, workdir, rel, a.Offset, a.Limit)
	if err != nil {
		return nil, mcpReadResult{}, err
	}
	out := mcpReadResult{
		Sandbox:   sandbox,
		Path:      abs,
		Bytes:     total,
		Truncated: truncated,
		Content:   content,
	}
	return mcpText("read %d bytes from %s", len(content), abs), out, nil
}

func (m *mcpTools) agentWrite(ctx context.Context, _ *mcp.CallToolRequest, a mcpWriteArgs) (*mcp.CallToolResult, mcpWriteResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpWriteResult{}, err
	}
	rel, err := safeToolPath(a.Path)
	if err != nil {
		return nil, mcpWriteResult{}, err
	}
	abs := absUnderWorkdir(workdir, rel)
	if a.CreateOnly {
		if _, _, _, err := m.readTextFile(ctx, sandbox, workdir, rel, 0, 1); err == nil {
			return nil, mcpWriteResult{}, fmt.Errorf("refusing to overwrite existing file %q", abs)
		}
	}
	tarBytes, err := tarSingleFile(rel, []byte(a.Content), a.Mode)
	if err != nil {
		return nil, mcpWriteResult{}, err
	}
	if _, err := m.importTar(ctx, sandbox, workdir, tarBytes); err != nil {
		return nil, mcpWriteResult{}, err
	}
	out := mcpWriteResult{Sandbox: sandbox, Path: abs, Bytes: len(a.Content)}
	return mcpText("wrote %d bytes to %s", len(a.Content), abs), out, nil
}

func (m *mcpTools) agentEdit(ctx context.Context, _ *mcp.CallToolRequest, a mcpEditArgs) (*mcp.CallToolResult, mcpEditResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpEditResult{}, err
	}
	if a.Old == "" {
		return nil, mcpEditResult{}, errors.New("old must be non-empty")
	}
	rel, err := safeToolPath(a.Path)
	if err != nil {
		return nil, mcpEditResult{}, err
	}
	abs := absUnderWorkdir(workdir, rel)
	content, _, truncated, err := m.readTextFile(ctx, sandbox, workdir, rel, 0, 5<<20)
	if err != nil {
		return nil, mcpEditResult{}, err
	}
	if truncated {
		return nil, mcpEditResult{}, fmt.Errorf("file too large for Edit: %s", abs)
	}
	count := strings.Count(content, a.Old)
	if count == 0 {
		return nil, mcpEditResult{}, fmt.Errorf("old text not found in %s\n%s",
			abs, formatEditHint(content, a.Old))
	}
	if !a.ReplaceAll && count != 1 {
		return nil, mcpEditResult{}, fmt.Errorf(
			"old text matched %d times in %s; set replace_all or extend old with surrounding context\n%s",
			count, abs, formatEditMatches(content, a.Old))
	}
	next := strings.Replace(content, a.Old, a.New, 1)
	replacements := 1
	if a.ReplaceAll {
		next = strings.ReplaceAll(content, a.Old, a.New)
		replacements = count
	}
	tarBytes, err := tarSingleFile(rel, []byte(next), "0644")
	if err != nil {
		return nil, mcpEditResult{}, err
	}
	if _, err := m.importTar(ctx, sandbox, workdir, tarBytes); err != nil {
		return nil, mcpEditResult{}, err
	}
	out := mcpEditResult{Sandbox: sandbox, Path: abs, Replacements: replacements}
	return mcpText("edited %s (%d replacements)", abs, replacements), out, nil
}

func (m *mcpTools) agentBash(ctx context.Context, _ *mcp.CallToolRequest, a mcpBashArgs) (*mcp.CallToolResult, mcpBashResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpBashResult{}, err
	}
	if strings.TrimSpace(a.Command) == "" {
		return nil, mcpBashResult{}, errors.New("command is required")
	}
	relCwd, err := safeToolDir(a.Cwd)
	if err != nil {
		return nil, mcpBashResult{}, err
	}
	cwd := absUnderWorkdir(workdir, relCwd)
	cd, err := startCommand(ctx, m.c, sandbox, []string{"sh", "-lc", a.Command}, a.Env, cwd, a.CredentialCatalog)
	if err != nil {
		return nil, mcpBashResult{}, err
	}
	if a.Background {
		out := mcpBashResult{Sandbox: sandbox, CommandID: cd.CommandID, Phase: cd.Phase}
		return mcpText("running %s", cd.CommandID), out, nil
	}
	timeout := time.Duration(a.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	maxBytes := a.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = 200 << 10
	}
	out, err := m.collectCommand(ctx, sandbox, cd.CommandID, timeout, maxBytes)
	if err != nil {
		return nil, mcpBashResult{}, err
	}
	out.Sandbox = sandbox
	out.CommandID = cd.CommandID
	return mcpText("%s", out.Phase), out, nil
}

func (m *mcpTools) agentMonitor(ctx context.Context, _ *mcp.CallToolRequest, a mcpMonitorArgs) (*mcp.CallToolResult, mcpMonitorResult, error) {
	sandbox, _, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpMonitorResult{}, err
	}
	timeout := time.Duration(a.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxLines := a.MaxLines
	if maxLines <= 0 {
		maxLines = 200
	}
	deadline := time.Now().Add(timeout)
	cursor := a.Cursor
	out := mcpMonitorResult{Sandbox: sandbox, CommandID: a.CommandID, Phase: "running", Cursor: cursor}
	for {
		logs, err := fetchLogsCursor(ctx, m.c, sandbox, a.CommandID, cursor)
		if err != nil {
			return nil, mcpMonitorResult{}, err
		}
		cursor = logs.NextCursor
		out.Cursor = cursor
		out.Phase = logs.Phase
		out.ExitCode = logs.ExitCode
		if logs.Bytes != "" {
			for _, line := range strings.SplitAfter(logs.Bytes, "\n") {
				if line == "" {
					continue
				}
				if len(out.Events) >= maxLines {
					out.Truncated = true
					break
				}
				out.Events = append(out.Events, mcpMonitorEvent{Stream: "combined", Text: line})
			}
		}
		if out.Phase != "running" || len(out.Events) > 0 || out.Truncated {
			break
		}
		if time.Now().After(deadline) {
			out.TimedOut = true
			break
		}
		select {
		case <-ctx.Done():
			return nil, mcpMonitorResult{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return mcpText("%d events", len(out.Events)), out, nil
}

func (m *mcpTools) agentGlob(ctx context.Context, _ *mcp.CallToolRequest, a mcpGlobArgs) (*mcp.CallToolResult, mcpGlobResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpGlobResult{}, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 500
	}
	relRoot, err := safeToolDir(a.Path)
	if err != nil {
		return nil, mcpGlobResult{}, err
	}
	root := absUnderWorkdir(workdir, relRoot)
	cmd := "find " + shQuote(root) + " -type f"
	run, err := m.runShell(ctx, sandbox, cmd, workdir, 30*time.Second, 2<<20)
	if err != nil {
		return nil, mcpGlobResult{}, err
	}
	re, err := globRegexp(a.Pattern)
	if err != nil {
		return nil, mcpGlobResult{}, err
	}
	out := mcpGlobResult{Sandbox: sandbox}
	prefix := workdir + "/"
	for _, line := range strings.Split(run.Output, "\n") {
		line = strings.TrimSpace(line)
		// `find <abs-root>` returns absolute paths; surface them
		// relative to workdir so the caller can compose them with
		// other tool args without re-rooting.
		line = strings.TrimPrefix(line, prefix)
		line = strings.TrimPrefix(line, "./")
		if line == "" {
			continue
		}
		if re.MatchString(line) {
			if len(out.Matches) >= limit {
				out.Truncated = true
				break
			}
			out.Matches = append(out.Matches, line)
		}
	}
	return mcpText("%d matches", len(out.Matches)), out, nil
}

func (m *mcpTools) agentGrep(ctx context.Context, _ *mcp.CallToolRequest, a mcpGrepArgs) (*mcp.CallToolResult, mcpGrepResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpGrepResult{}, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 200
	}
	flags := "-RInE"
	if a.IgnoreCase {
		flags += "i"
	}
	relRoot, err := safeToolDir(a.Path)
	if err != nil {
		return nil, mcpGrepResult{}, err
	}
	root := absUnderWorkdir(workdir, relRoot)
	parts := []string{"grep", flags}
	if a.Glob != "" {
		parts = append(parts, "--include="+shQuote(a.Glob))
	}
	parts = append(parts, "--", shQuote(a.Pattern), shQuote(root))
	run, err := m.runShell(ctx, sandbox, strings.Join(parts, " "), workdir, 30*time.Second, 2<<20)
	if err != nil {
		return nil, mcpGrepResult{}, err
	}
	out := mcpGrepResult{Sandbox: sandbox}
	prefix := workdir + "/"
	for _, line := range strings.Split(run.Output, "\n") {
		if line == "" {
			continue
		}
		match := parseGrepLine(line)
		if match.Path == "" {
			continue
		}
		match.Path = strings.TrimPrefix(match.Path, prefix)
		if len(out.Matches) >= limit {
			out.Truncated = true
			break
		}
		out.Matches = append(out.Matches, match)
	}
	return mcpText("%d matches", len(out.Matches)), out, nil
}

// maxAgentDownloadBytes caps the raw tar size returned to the MCP
// caller before base64 encoding. The MCP framing has no streaming
// affordance, so a multi-hundred-MiB response would balloon the
// stdio buffer and stall the host. When the cap is exceeded the
// caller must narrow `paths` (or use the management ExportFiles tool
// with a real artifact handle once that lands).
const maxAgentDownloadBytes = 50 << 20

func (m *mcpTools) agentUpload(ctx context.Context, _ *mcp.CallToolRequest, a mcpUploadArgs) (*mcp.CallToolResult, mcpUploadResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpUploadResult{}, err
	}
	relDest, err := safeToolDir(a.Dest)
	if err != nil {
		return nil, mcpUploadResult{}, err
	}
	dest := absUnderWorkdir(workdir, relDest)
	tarBytes, err := base64.StdEncoding.DecodeString(a.TarBase64)
	if err != nil {
		return nil, mcpUploadResult{}, fmt.Errorf("decode tar_base64: %w", err)
	}
	if !a.Overwrite {
		if err := m.preflightUploadOverwrite(ctx, sandbox, workdir, relDest, tarBytes); err != nil {
			return nil, mcpUploadResult{}, err
		}
	}
	resp, err := m.importTar(ctx, sandbox, dest, tarBytes)
	if err != nil {
		return nil, mcpUploadResult{}, err
	}
	out := mcpUploadResult{Sandbox: sandbox, Dest: dest, Bytes: resp.Bytes}
	return mcpText("uploaded %d bytes to %s", resp.Bytes, dest), out, nil
}

func (m *mcpTools) agentDownload(ctx context.Context, _ *mcp.CallToolRequest, a mcpDownloadArgs) (*mcp.CallToolResult, mcpDownloadResult, error) {
	sandbox, workdir, err := m.ensureSandbox(ctx, a.Sandbox)
	if err != nil {
		return nil, mcpDownloadResult{}, err
	}
	relSrc, err := safeToolDir(a.SrcDir)
	if err != nil {
		return nil, mcpDownloadResult{}, err
	}
	srcDir := absUnderWorkdir(workdir, relSrc)
	cleanPaths := make([]string, 0, len(a.Paths))
	for _, p := range a.Paths {
		clean, err := safeToolPath(p)
		if err != nil {
			return nil, mcpDownloadResult{}, err
		}
		cleanPaths = append(cleanPaths, clean)
	}
	tarBytes, err := m.exportTar(ctx, sandbox, srcDir, cleanPaths)
	if err != nil {
		return nil, mcpDownloadResult{}, err
	}
	if len(tarBytes) > maxAgentDownloadBytes {
		return nil, mcpDownloadResult{}, fmt.Errorf(
			"download exceeds %d byte cap (got %d); narrow `paths` to the files you need",
			maxAgentDownloadBytes, len(tarBytes))
	}
	out := mcpDownloadResult{
		Sandbox:   sandbox,
		TarBase64: base64.StdEncoding.EncodeToString(tarBytes),
		Bytes:     len(tarBytes),
	}
	return mcpText("downloaded %d bytes", len(tarBytes)), out, nil
}

func (m *mcpTools) create(ctx context.Context, _ *mcp.CallToolRequest, a mcpCreateArgs) (*mcp.CallToolResult, mcpCreateResult, error) {
	body := map[string]any{
		"image": a.Image, "tier": a.Tier, "disk_gb": a.DiskGB,
		"name": a.Name, "auto_delete_hours": a.AutoDeleteHours,
	}
	if a.CPU != "" {
		body["cpu"] = a.CPU
	}
	if a.Memory != "" {
		body["memory"] = a.Memory
	}
	if len(a.CredentialCatalog) > 0 {
		body["credential_catalog"] = a.CredentialCatalog
	}
	var resp struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := m.c.PostJSON(ctx, "/v1/sandboxes", body, &resp); err != nil {
		return nil, mcpCreateResult{}, err
	}
	out := mcpCreateResult{ID: resp.ID, Slug: resp.Name, State: resp.State}
	return mcpText("created %s (%s)", resp.ID, resp.State), out, nil
}

func (m *mcpTools) run(ctx context.Context, _ *mcp.CallToolRequest, a mcpRunArgs) (*mcp.CallToolResult, mcpRunResult, error) {
	body := map[string]any{
		"argv": a.Argv, "env": a.Env, "cwd": a.Cwd, "detach": true,
	}
	if len(a.CredentialCatalog) > 0 {
		body["credential_catalog"] = a.CredentialCatalog
	}
	var resp struct {
		CommandID string `json:"command_id"`
		Phase     string `json:"phase"`
	}
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) + "/commands"
	if err := m.c.PostJSON(ctx, path, body, &resp); err != nil {
		return nil, mcpRunResult{}, err
	}
	return mcpText("running %s", resp.CommandID), mcpRunResult{
		CommandID: resp.CommandID, Phase: resp.Phase,
	}, nil
}

func (m *mcpTools) wait(ctx context.Context, _ *mcp.CallToolRequest, a mcpWaitArgs) (*mcp.CallToolResult, mcpWaitResult, error) {
	timeout := a.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		var resp struct {
			Phase    string `json:"phase"`
			ExitCode *int   `json:"exit_code,omitempty"`
		}
		path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) +
			"/commands/" + url.PathEscape(a.CommandID)
		if err := m.c.GetJSON(ctx, path, &resp); err != nil {
			return nil, mcpWaitResult{}, err
		}
		if resp.Phase != "running" {
			return mcpText("%s", resp.Phase), mcpWaitResult{
				Phase: resp.Phase, ExitCode: resp.ExitCode,
			}, nil
		}
		if time.Now().After(deadline) {
			return mcpText("still running after %ds", timeout),
				mcpWaitResult{Phase: "running"}, nil
		}
		select {
		case <-ctx.Done():
			return nil, mcpWaitResult{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (m *mcpTools) logs(ctx context.Context, _ *mcp.CallToolRequest, a mcpLogsArgs) (*mcp.CallToolResult, mcpLogsResult, error) {
	q := url.Values{}
	q.Set("cursor", strconv.FormatInt(a.SinceCursor, 10))
	q.Set("stream", "false")
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) +
		"/commands/" + url.PathEscape(a.CommandID) + "/logs?" + q.Encode()
	var resp mcpLogsResult
	if err := m.c.GetJSON(ctx, path, &resp); err != nil {
		return nil, mcpLogsResult{}, err
	}
	return mcpText("%d bytes (cursor=%d)", len(resp.Bytes), resp.NextCursor), resp, nil
}

func (m *mcpTools) export(ctx context.Context, _ *mcp.CallToolRequest, a mcpExportArgs) (*mcp.CallToolResult, mcpExportResult, error) {
	body := map[string]any{"src_dir": a.SrcDir, "paths": a.Paths}
	b, _ := json.Marshal(body)
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) + "/files/export"
	resp, err := m.c.DoRaw(ctx, http.MethodPost, path,
		bytes.NewReader(b), "application/json")
	if err != nil {
		return nil, mcpExportResult{}, err
	}
	defer resp.Body.Close()
	tarBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, mcpExportResult{}, err
	}
	out := mcpExportResult{
		TarBase64: base64.StdEncoding.EncodeToString(tarBytes),
		Bytes:     len(tarBytes),
	}
	return mcpText("exported %d bytes", len(tarBytes)), out, nil
}

func (m *mcpTools) imp(ctx context.Context, _ *mcp.CallToolRequest, a mcpImportArgs) (*mcp.CallToolResult, mcpImportResult, error) {
	tarBytes, err := base64.StdEncoding.DecodeString(a.TarBase64)
	if err != nil {
		return nil, mcpImportResult{}, fmt.Errorf("decode tar_base64: %w", err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if a.Dest != "" {
		_ = mw.WriteField("dest", a.Dest)
	}
	fw, err := mw.CreateFormFile("tarball", "import.tar")
	if err != nil {
		return nil, mcpImportResult{}, err
	}
	if _, err := fw.Write(tarBytes); err != nil {
		return nil, mcpImportResult{}, err
	}
	mw.Close()

	var resp mcpImportResult
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) + "/files/import"
	if err := m.c.Do(ctx, http.MethodPost, path,
		&buf, mw.FormDataContentType(), &resp); err != nil {
		return nil, mcpImportResult{}, err
	}
	return mcpText("imported %d bytes to %s", resp.Bytes, resp.Dest), resp, nil
}

func (m *mcpTools) del(ctx context.Context, _ *mcp.CallToolRequest, a mcpDeleteArgs) (*mcp.CallToolResult, mcpDeleteResult, error) {
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox)
	if err := m.c.Do(ctx, http.MethodDelete, path, nil, "", nil); err != nil {
		return nil, mcpDeleteResult{}, err
	}
	return mcpText("deleted %s", a.Sandbox), mcpDeleteResult{OK: true}, nil
}

// ---- list / get / start / stop / extend / convert / kill ----

type mcpListArgs struct {
	NameFilter string `json:"name,omitempty" mcp:"optional name filter"`
}
type mcpSandboxSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	State string `json:"state"`
	Tier  string `json:"tier,omitempty"`
}
type mcpListResult struct {
	Sandboxes []mcpSandboxSummary `json:"sandboxes"`
}

func (m *mcpTools) list(ctx context.Context, _ *mcp.CallToolRequest, a mcpListArgs) (*mcp.CallToolResult, mcpListResult, error) {
	q := url.Values{}
	if a.NameFilter != "" {
		q.Set("name", a.NameFilter)
	}
	path := "/v1/sandboxes"
	if e := q.Encode(); e != "" {
		path += "?" + e
	}
	var rows []mcpSandboxSummary
	if err := m.c.GetJSON(ctx, path, &rows); err != nil {
		return nil, mcpListResult{}, err
	}
	return mcpText("%d sandboxes", len(rows)), mcpListResult{Sandboxes: rows}, nil
}

type mcpGetArgs struct {
	Sandbox string `json:"sandbox" mcp:"sandbox id or slug"`
}
type mcpGetResult struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	State    string `json:"state"`
	Tier     string `json:"tier,omitempty"`
	DiskGB   int    `json:"disk_gb,omitempty"`
	Deadline string `json:"deadline,omitempty"`
}

func (m *mcpTools) get(ctx context.Context, _ *mcp.CallToolRequest, a mcpGetArgs) (*mcp.CallToolResult, mcpGetResult, error) {
	var resp mcpGetResult
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox)
	if err := m.c.GetJSON(ctx, path, &resp); err != nil {
		return nil, mcpGetResult{}, err
	}
	return mcpText("%s (%s)", resp.Name, resp.State), resp, nil
}

type mcpVerbArgs struct {
	Sandbox string `json:"sandbox"`
}
type mcpVerbResult struct {
	State string `json:"state"`
}

func (m *mcpTools) start(ctx context.Context, _ *mcp.CallToolRequest, a mcpVerbArgs) (*mcp.CallToolResult, mcpVerbResult, error) {
	return m.lifecycleVerb(ctx, a.Sandbox, "start")
}
func (m *mcpTools) stop(ctx context.Context, _ *mcp.CallToolRequest, a mcpVerbArgs) (*mcp.CallToolResult, mcpVerbResult, error) {
	return m.lifecycleVerb(ctx, a.Sandbox, "stop")
}
func (m *mcpTools) lifecycleVerb(ctx context.Context, sandbox, verb string) (*mcp.CallToolResult, mcpVerbResult, error) {
	var resp mcpVerbResult
	path := "/v1/sandboxes/" + url.PathEscape(sandbox) + "/" + verb
	if err := m.c.PostJSON(ctx, path, nil, &resp); err != nil {
		return nil, mcpVerbResult{}, err
	}
	return mcpText("%s → %s", verb, resp.State), resp, nil
}

type mcpExtendArgs struct {
	Sandbox         string `json:"sandbox"`
	AutoDeleteHours int    `json:"auto_delete_hours,omitempty" mcp:"hours to push the deadline; default 24"`
}
type mcpExtendResult struct {
	State    string `json:"state"`
	Deadline string `json:"deadline,omitempty"`
}

func (m *mcpTools) extend(ctx context.Context, _ *mcp.CallToolRequest, a mcpExtendArgs) (*mcp.CallToolResult, mcpExtendResult, error) {
	hours := a.AutoDeleteHours
	if hours <= 0 {
		hours = 24
	}
	body := map[string]any{"auto_delete_hours": hours}
	var resp mcpExtendResult
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) + "/extend"
	if err := m.c.PostJSON(ctx, path, body, &resp); err != nil {
		return nil, mcpExtendResult{}, err
	}
	return mcpText("extended to %s", resp.Deadline), resp, nil
}

type mcpConvertArgs struct {
	Sandbox         string `json:"sandbox"`
	Tier            string `json:"tier" mcp:"ephemeral or persistent"`
	AutoDeleteHours int    `json:"auto_delete_hours,omitempty" mcp:"required when tier=ephemeral"`
}
type mcpConvertResult struct {
	State string `json:"state"`
	Tier  string `json:"tier,omitempty"`
}

func (m *mcpTools) convert(ctx context.Context, _ *mcp.CallToolRequest, a mcpConvertArgs) (*mcp.CallToolResult, mcpConvertResult, error) {
	if a.Tier != "ephemeral" && a.Tier != "persistent" {
		return nil, mcpConvertResult{}, fmt.Errorf("tier must be ephemeral or persistent")
	}
	body := map[string]any{"tier": a.Tier}
	if a.Tier == "ephemeral" {
		if a.AutoDeleteHours <= 0 {
			return nil, mcpConvertResult{}, fmt.Errorf("auto_delete_hours is required when converting to ephemeral")
		}
		body["auto_delete_hours"] = a.AutoDeleteHours
	}
	var resp mcpConvertResult
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) + "/convert"
	if err := m.c.PostJSON(ctx, path, body, &resp); err != nil {
		return nil, mcpConvertResult{}, err
	}
	return mcpText("converted → %s", a.Tier), resp, nil
}

type mcpKillArgs struct {
	Sandbox   string `json:"sandbox"`
	CommandID string `json:"command_id"`
}
type mcpKillResult struct {
	OK bool `json:"ok"`
}

func (m *mcpTools) kill(ctx context.Context, _ *mcp.CallToolRequest, a mcpKillArgs) (*mcp.CallToolResult, mcpKillResult, error) {
	path := "/v1/sandboxes/" + url.PathEscape(a.Sandbox) +
		"/commands/" + url.PathEscape(a.CommandID)
	if err := m.c.Do(ctx, http.MethodDelete, path, nil, "", nil); err != nil {
		return nil, mcpKillResult{}, err
	}
	return mcpText("killed %s", a.CommandID), mcpKillResult{OK: true}, nil
}

// ---- helpers ----

func parseSandboxAliases(values []string) (map[string]string, error) {
	aliases := map[string]string{}
	for _, value := range values {
		k, v, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("invalid --sandbox %q; expected alias=id-or-slug", value)
		}
		aliases[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return aliases, nil
}

func (m *mcpTools) aliasFor(id, name string) string {
	for alias, target := range m.aliases {
		if target == id || target == name {
			return alias
		}
	}
	return ""
}

func (m *mcpTools) resolveSandbox(selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", errors.New("sandbox is required")
	}
	if target := m.aliases[selector]; target != "" {
		return target, nil
	}
	return selector, nil
}

// ensureSandbox resolves the selector, auto-starts the sandbox if
// allowed, and returns the canonical id plus the resolved workdir.
// Sandboxes served by older sandboxd versions that omit workdir fall
// back to fallbackWorkdir so the MCP keeps working through the
// rollout window.
func (m *mcpTools) ensureSandbox(ctx context.Context, selector string) (string, string, error) {
	sandbox, err := m.resolveSandbox(selector)
	if err != nil {
		return "", "", err
	}
	var sb sandboxDTO
	if err := m.c.GetJSON(ctx, sbPath(sandbox), &sb); err != nil {
		return "", "", err
	}
	if sb.State == "stopped" {
		if !m.autoStart {
			return "", "", fmt.Errorf("sandbox %q is stopped and auto-start is disabled", selector)
		}
		if _, _, err := m.lifecycleVerb(ctx, sandbox, "start"); err != nil {
			return "", "", err
		}
	}
	id := sb.ID
	if id == "" {
		id = sandbox
	}
	wd := sb.Workdir
	if wd == "" {
		wd = fallbackWorkdir
	}
	return id, wd, nil
}

// absUnderWorkdir joins a validated relative path with the resolved
// workdir to produce the absolute container path used in API calls and
// surfaced to the MCP caller. relPath must already have passed through
// safeToolPath / safeToolDir.
func absUnderWorkdir(workdir, relPath string) string {
	if relPath == "" {
		return workdir
	}
	return path.Join(workdir, relPath)
}

func safeToolPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "", errors.New("path is required")
	}
	if path.IsAbs(p) {
		return "", fmt.Errorf("absolute paths are not allowed: %s", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes sandbox workdir: %s", p)
	}
	return clean, nil
}

func safeToolDir(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "", nil
	}
	if path.IsAbs(p) {
		return "", fmt.Errorf("absolute paths are not allowed: %s", p)
	}
	clean := path.Clean(p)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes sandbox workdir: %s", p)
	}
	return clean, nil
}

func (m *mcpTools) readTextFile(ctx context.Context, sandbox, workdir, file string, offset, limit int) (string, int, bool, error) {
	rel, err := safeToolPath(file)
	if err != nil {
		return "", 0, false, err
	}
	if offset < 0 {
		return "", 0, false, errors.New("offset must be >= 0")
	}
	if limit <= 0 {
		limit = 20000
	}
	if limit > 5<<20 {
		return "", 0, false, errors.New("limit must be <= 5242880")
	}
	tarBytes, err := m.exportTar(ctx, sandbox, workdir, []string{rel})
	if err != nil {
		return "", 0, false, err
	}
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", 0, false, err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 5<<20+1))
		if err != nil {
			return "", 0, false, err
		}
		if len(body) > 5<<20 {
			return "", 0, false, fmt.Errorf("file too large for Read: %s", file)
		}
		if !utf8.Valid(body) {
			return "", len(body), false, fmt.Errorf("file is not valid UTF-8 text: %s", rel)
		}
		total := len(body)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		return string(body[offset:end]), total, end < total, nil
	}
	return "", 0, false, fmt.Errorf("file not found in export: %s", rel)
}

func tarSingleFile(file string, content []byte, mode string) ([]byte, error) {
	file, err := safeToolPath(file)
	if err != nil {
		return nil, err
	}
	perm := int64(0o644)
	if mode != "" {
		parsed, err := strconv.ParseInt(strings.TrimPrefix(mode, "0o"), 8, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid mode %q", mode)
		}
		perm = parsed
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: file,
		Mode: perm,
		Size: int64(len(content)),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// exportTar runs the API export with an absolute src_dir already
// resolved against the sandbox workdir. paths must already be relative
// to that src_dir; safeToolPath callers feed it directly.
func (m *mcpTools) exportTar(ctx context.Context, sandbox, srcDirAbs string, paths []string) ([]byte, error) {
	if srcDirAbs == "" || !path.IsAbs(srcDirAbs) {
		return nil, fmt.Errorf("internal: src_dir must be absolute, got %q", srcDirAbs)
	}
	body := map[string]any{"src_dir": srcDirAbs, "paths": paths}
	b, _ := json.Marshal(body)
	resp, err := m.c.DoRaw(ctx, http.MethodPost, sbPath(sandbox)+"/files/export",
		bytes.NewReader(b), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// importTar posts the multipart form. destAbs must be absolute (it is
// what the API endpoint enforces server-side). The archive's entries
// stay relative to destAbs.
func (m *mcpTools) importTar(ctx context.Context, sandbox, destAbs string, tarBytes []byte) (mcpImportResult, error) {
	if destAbs == "" || !path.IsAbs(destAbs) {
		return mcpImportResult{}, fmt.Errorf("internal: dest must be absolute, got %q", destAbs)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("dest", destAbs)
	fw, err := mw.CreateFormFile("tarball", "import.tar")
	if err != nil {
		return mcpImportResult{}, err
	}
	if _, err := fw.Write(tarBytes); err != nil {
		return mcpImportResult{}, err
	}
	if err := mw.Close(); err != nil {
		return mcpImportResult{}, err
	}
	var resp mcpImportResult
	if err := m.c.Do(ctx, http.MethodPost, sbPath(sandbox)+"/files/import",
		&buf, mw.FormDataContentType(), &resp); err != nil {
		return mcpImportResult{}, err
	}
	return resp, nil
}

func (m *mcpTools) collectCommand(ctx context.Context, sandbox, cmdID string, timeout time.Duration, maxBytes int) (mcpBashResult, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if maxBytes <= 0 {
		maxBytes = 200 << 10
	}
	deadline := time.Now().Add(timeout)
	out := mcpBashResult{CommandID: cmdID, Phase: "running"}
	var buf strings.Builder
	var cursor int64
	for {
		logs, err := fetchLogsCursor(ctx, m.c, sandbox, cmdID, cursor)
		if err != nil {
			return out, err
		}
		cursor = logs.NextCursor
		out.NextCursor = cursor
		out.Phase = logs.Phase
		out.ExitCode = logs.ExitCode
		if logs.Bytes != "" && buf.Len() < maxBytes {
			remain := maxBytes - buf.Len()
			if len(logs.Bytes) > remain {
				buf.WriteString(logs.Bytes[:remain])
				out.Truncated = true
			} else {
				buf.WriteString(logs.Bytes)
			}
		} else if logs.Bytes != "" {
			out.Truncated = true
		}
		if out.Phase != "running" {
			out.Output = buf.String()
			return out, nil
		}
		if time.Now().After(deadline) {
			out.Output = buf.String()
			out.TimedOut = true
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// runShell runs an internal helper command with cwd already resolved
// to an absolute container path. Callers feed it the sandbox workdir
// (or workdir/sub) so we never depend on the runtime's default cwd.
func (m *mcpTools) runShell(ctx context.Context, sandbox, command, cwdAbs string, timeout time.Duration, maxBytes int) (mcpBashResult, error) {
	cd, err := startCommand(ctx, m.c, sandbox, []string{"sh", "-lc", command}, nil, cwdAbs, nil)
	if err != nil {
		return mcpBashResult{}, err
	}
	out, err := m.collectCommand(ctx, sandbox, cd.CommandID, timeout, maxBytes)
	if err != nil {
		return out, err
	}
	out.CommandID = cd.CommandID
	out.Sandbox = sandbox
	return out, nil
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, errors.New("pattern is required")
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '/', '.':
			b.WriteByte('\\')
			b.WriteByte(pattern[i])
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func parseGrepLine(line string) mcpGrepMatch {
	first := strings.IndexByte(line, ':')
	if first < 0 {
		return mcpGrepMatch{}
	}
	second := strings.IndexByte(line[first+1:], ':')
	if second < 0 {
		return mcpGrepMatch{}
	}
	second += first + 1
	lineNo, err := strconv.Atoi(line[first+1 : second])
	if err != nil {
		return mcpGrepMatch{}
	}
	return mcpGrepMatch{
		Path: strings.TrimPrefix(line[:first], "./"),
		Line: lineNo,
		Text: line[second+1:],
	}
}

func mcpText(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

// formatEditMatches returns a short snippet for each occurrence of old
// in content so an Edit caller can disambiguate by extending old. Only
// the first few matches are shown to keep the error compact.
func formatEditMatches(content, old string) string {
	const maxMatches = 5
	const ctx = 40
	var b strings.Builder
	b.WriteString("matches:")
	idx := 0
	shown := 0
	for shown < maxMatches {
		i := strings.Index(content[idx:], old)
		if i < 0 {
			break
		}
		hit := idx + i
		start := hit - ctx
		if start < 0 {
			start = 0
		}
		end := hit + len(old) + ctx
		if end > len(content) {
			end = len(content)
		}
		line := lineNumber(content, hit)
		fmt.Fprintf(&b, "\n  L%d: %s", line, snippetEscape(content[start:end]))
		shown++
		idx = hit + len(old)
		if idx >= len(content) {
			break
		}
	}
	return b.String()
}

// formatEditHint is shown when old is not found at all. It surfaces a
// short head-of-file snippet so the agent can spot whitespace or
// encoding mismatches without re-reading the file.
func formatEditHint(content, old string) string {
	preview := content
	if len(preview) > 200 {
		preview = preview[:200] + "…"
	}
	return fmt.Sprintf("file head:\n  %s\nold (escaped): %s",
		snippetEscape(preview), snippetEscape(old))
}

func lineNumber(s string, offset int) int {
	if offset > len(s) {
		offset = len(s)
	}
	return strings.Count(s[:offset], "\n") + 1
}

func snippetEscape(s string) string {
	r := strings.NewReplacer("\n", "\\n", "\t", "\\t", "\r", "\\r")
	return r.Replace(s)
}

// preflightUploadOverwrite walks the upload archive's tar entries,
// resolves each one against destAbs, and probes the sandbox for an
// existing file. The probe export uses a single API call covering
// every candidate path so the round-trip is one request even for
// multi-file uploads. When any candidate already exists the upload is
// rejected — overwrite=true bypasses this check.
func (m *mcpTools) preflightUploadOverwrite(ctx context.Context, sandbox, workdir, relDest string, tarBytes []byte) error {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	probePaths := []string{}
	rels := []string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read upload tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel := hdr.Name
		if relDest != "" {
			rel = path.Join(relDest, hdr.Name)
		}
		rel = path.Clean(rel)
		if path.IsAbs(rel) || strings.HasPrefix(rel, "../") {
			// The server-side validator will also reject this; surface
			// the same error early so we don't hide the real cause.
			return fmt.Errorf("upload entry escapes dest: %s", hdr.Name)
		}
		probePaths = append(probePaths, rel)
		rels = append(rels, rel)
	}
	if len(probePaths) == 0 {
		return nil
	}
	tarOut, err := m.exportTar(ctx, sandbox, workdir, probePaths)
	if err != nil {
		// A 4xx here means tar refused — typically because no candidate
		// exists yet, which is exactly what we want.
		return nil
	}
	existing := map[string]bool{}
	pr := tar.NewReader(bytes.NewReader(tarOut))
	for {
		hdr, err := pr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		existing[path.Clean(hdr.Name)] = true
	}
	for _, rel := range rels {
		if existing[rel] {
			return fmt.Errorf("upload would overwrite existing file %q; pass overwrite=true",
				absUnderWorkdir(workdir, rel))
		}
	}
	return nil
}
