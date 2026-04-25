package commands

import (
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
	"strconv"
	"time"

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
	var apiURL string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP stdio server exposing this user's cellas as MCP tools.",
		Long: `Run a Model Context Protocol server over stdio.

Exposes seven tools to the MCP host:
  cella_create / cella_run / cella_wait / cella_logs /
  cella_export / cella_import / cella_delete

The token at ~/.config/latere/token.json (written by 'latere auth login')
is used for every call. A missing token starts the server anyway so the
auth failure surfaces on first tool use rather than at boot.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := api.NewClient(apiURL)
			return runMCPServer(cmd.Context(), c)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override cella base URL")
	return cmd
}

// runMCPServer wires the MCP tools onto a stdio server and runs until
// the host disconnects (or ctx is cancelled).
func runMCPServer(ctx context.Context, c *api.Client) error {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "latere-cella",
		Version: "0.2.0",
	}, nil)

	mt := &mcpTools{c: c}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cella_create",
		Description: "Create a new cella. Returns its id and slug.",
	}, mt.create)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cella_run",
		Description: "Start a command in the background; returns command_id immediately.",
	}, mt.run)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cella_wait",
		Description: "Poll a command until it terminates or timeout_seconds passes.",
	}, mt.wait)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cella_logs",
		Description: "Read command logs starting at since_cursor. Returns bytes + next_cursor.",
	}, mt.logs)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cella_export",
		Description: "Tar files from the cella workspace; returns base64-encoded tar.",
	}, mt.export)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cella_import",
		Description: "Upload a base64-encoded tar into the cella workspace.",
	}, mt.imp)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cella_delete",
		Description: "Delete a cella.",
	}, mt.del)

	t := &mcp.LoggingTransport{Transport: &mcp.StdioTransport{}, Writer: os.Stderr}
	return srv.Run(ctx, t)
}

// ---- tool args / results ----

type mcpCreateArgs struct {
	Image           string `json:"image" mcp:"container image, e.g. ghcr.io/latere-ai/sandbox-base:main"`
	Tier            string `json:"tier,omitempty" mcp:"ephemeral (default) or persistent"`
	DiskGB          int    `json:"disk_gb,omitempty"`
	Name            string `json:"name,omitempty" mcp:"optional human slug"`
	AutoDeleteHours int    `json:"auto_delete_hours,omitempty"`
}
type mcpCreateResult struct {
	ID    string `json:"id"`
	Slug  string `json:"slug,omitempty"`
	State string `json:"state"`
}

type mcpRunArgs struct {
	Cella string            `json:"cella" mcp:"cella id or slug"`
	Argv  []string          `json:"argv"`
	Env   map[string]string `json:"env,omitempty"`
	Cwd   string            `json:"cwd,omitempty"`
}
type mcpRunResult struct {
	CommandID string `json:"command_id"`
	Phase     string `json:"phase"`
}

type mcpWaitArgs struct {
	Cella          string `json:"cella"`
	CommandID      string `json:"command_id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" mcp:"max poll seconds; 0 = 60"`
}
type mcpWaitResult struct {
	Phase    string `json:"phase"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

type mcpLogsArgs struct {
	Cella       string `json:"cella"`
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
	Cella  string   `json:"cella"`
	SrcDir string   `json:"src_dir,omitempty"`
	Paths  []string `json:"paths,omitempty"`
}
type mcpExportResult struct {
	TarBase64 string `json:"tar_base64"`
	Bytes     int    `json:"bytes"`
}

type mcpImportArgs struct {
	Cella     string `json:"cella"`
	Dest      string `json:"dest,omitempty"`
	TarBase64 string `json:"tar_base64"`
}
type mcpImportResult struct {
	Imported string `json:"imported"`
	Bytes    int64  `json:"bytes"`
	Dest     string `json:"dest"`
}

type mcpDeleteArgs struct {
	Cella string `json:"cella"`
}
type mcpDeleteResult struct {
	OK bool `json:"ok"`
}

// ---- tool handlers ----

type mcpTools struct {
	c *api.Client
}

func (m *mcpTools) create(ctx context.Context, _ *mcp.CallToolRequest, a mcpCreateArgs) (*mcp.CallToolResult, mcpCreateResult, error) {
	body := map[string]any{
		"image": a.Image, "tier": a.Tier, "disk_gb": a.DiskGB,
		"name": a.Name, "auto_delete_hours": a.AutoDeleteHours,
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
	var resp struct {
		CommandID string `json:"command_id"`
		Phase     string `json:"phase"`
	}
	path := "/v1/sandboxes/" + url.PathEscape(a.Cella) + "/commands"
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
		path := "/v1/sandboxes/" + url.PathEscape(a.Cella) +
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
	path := "/v1/sandboxes/" + url.PathEscape(a.Cella) +
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
	path := "/v1/sandboxes/" + url.PathEscape(a.Cella) + "/files/export"
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
	path := "/v1/sandboxes/" + url.PathEscape(a.Cella) + "/files/import"
	if err := m.c.Do(ctx, http.MethodPost, path,
		&buf, mw.FormDataContentType(), &resp); err != nil {
		return nil, mcpImportResult{}, err
	}
	return mcpText("imported %d bytes to %s", resp.Bytes, resp.Dest), resp, nil
}

func (m *mcpTools) del(ctx context.Context, _ *mcp.CallToolRequest, a mcpDeleteArgs) (*mcp.CallToolResult, mcpDeleteResult, error) {
	path := "/v1/sandboxes/" + url.PathEscape(a.Cella)
	if err := m.c.Do(ctx, http.MethodDelete, path, nil, "", nil); err != nil {
		return nil, mcpDeleteResult{}, err
	}
	return mcpText("deleted %s", a.Cella), mcpDeleteResult{OK: true}, nil
}

// ---- helpers ----

func mcpText(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

// silence unused-import in case the build is partial.
var _ = errors.New
