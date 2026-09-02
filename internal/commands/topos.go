// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

// ---- DTOs (subset of Topos API; keep loose so additive backend
//      changes don't break the CLI). ----

type agentDTO struct {
	ID                 string    `json:"id"`
	OrgID              string    `json:"org_id"`
	OwnerSub           string    `json:"owner_sub"`
	DisplayName        string    `json:"display_name"`
	Kind               string    `json:"kind"`
	WorkspaceRef       string    `json:"workspace_ref,omitempty"`
	CustomInstructions string    `json:"custom_instructions,omitempty"`
	PrincipalID        string    `json:"principal_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type listAgentsResponse struct {
	Agents []agentDTO `json:"agents"`
}

// createAgentRequest is the body for POST /v1/agents. org_id/owner are
// derived server-side from the bearer's claims, never sent by the client.
type createAgentRequest struct {
	DisplayName        string `json:"display_name"`
	Kind               string `json:"kind"`
	CustomInstructions string `json:"custom_instructions,omitempty"`
}

// sessionCreateRequest is the body for POST /v1/agents/{id}/sessions.
type sessionCreateRequest struct {
	Prompt string `json:"prompt"`
}

// sessionResultDTO mirrors the Topos harness SessionResult (kept loose so
// additive backend changes don't break the CLI).
type sessionResultDTO struct {
	SessionID  string `json:"session_id"`
	SandboxID  string `json:"sandbox_id"`
	Output     string `json:"output"`
	StopReason string `json:"stop_reason"`
	ToolCalls  int    `json:"tool_calls"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// ---- top-level ----

// newToposCmd is the canonical `latere topos …` command group. Topos is
// the Latere agent control plane at topos.latere.ai.
func newToposCmd() *cobra.Command {
	var (
		apiURL string
		local  bool
		dir    string
		model  string
		print  string
	)
	cmd := &cobra.Command{
		Use:   "topos",
		Short: "Topos: the Latere agent platform.",
		Long: `Topos is the Latere agent platform — run agents locally or on the hosted
control plane.

Run 'latere topos --local' to run an agent entirely on this machine: it works in
your current directory with your real files, using your local model credential
(ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN). No control plane, no login.

Run 'latere topos' (no --local) to use the hosted control plane: resume a running
session or start a new one; it signs you in on first use.`,
		Example: `  latere topos --local                      run an agent here, on your files
  latere topos --local -p "add a test for foo()"   one-shot, then exit
  latere topos                              hosted: open the platform`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if local {
				return runToposLocal(cmd.Context(), dir, model, print, cmd.Root().Version)
			}
			return runToposHome(cmd.Context(), apiURL)
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "run the agent on this machine (no control plane), like Claude Code")
	cmd.Flags().StringVar(&dir, "dir", ".", "working directory for --local (default: current directory)")
	cmd.Flags().StringVar(&model, "model", "", "model name for --local (default: the adapter's default)")
	cmd.Flags().StringVarP(&print, "print", "p", "", "with --local: run this one prompt, stream the result, and exit")
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	cmd.AddCommand(newToposLoginCmd())
	cmd.AddCommand(newToposAgentsCmd())
	cmd.AddCommand(newToposSessionCmd())
	cmd.AddCommand(newToposServeSandboxCmd())
	return cmd
}

// newToposLoginCmd implements `latere topos login`: choose and configure the
// model provider the local agent uses (Claude OAuth, an Anthropic API key, or
// Ollama). Running it explicitly lets you switch providers even when a
// CLAUDE_CODE_OAUTH_TOKEN is in your environment.
func newToposLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Choose the model provider for the local agent (latere topos --local).",
		Long: `Choose and configure the model provider for 'latere topos --local'.

Opens a picker: sign in with Claude (browser, no copy/paste), paste an Anthropic
API key, or use Ollama (local models, no key). The choice is saved to
~/.config/latere/topos-provider.json and takes precedence over any ambient
CLAUDE_CODE_OAUTH_TOKEN, so you can escape Claude Code's shared rate limit by
picking an API key (separate quota) or Ollama (fully local).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthPicker(cmd.Context())
		},
	}
}

// ---- agents subgroup ----

func newToposAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage Topos agents.",
		Long:  "List and inspect agents registered on the Topos control plane.",
		Example: `  latere topos agents list
  latere topos agents get agent_01hxy`,
	}
	cmd.AddCommand(newToposAgentsListCmd())
	cmd.AddCommand(newToposAgentsGetCmd())
	cmd.AddCommand(newToposAgentsCreateCmd())
	return cmd
}

// newToposAgentsCreateCmd implements `latere topos agents create`.
func newToposAgentsCreateCmd() *cobra.Command {
	var (
		apiURL       string
		name         string
		kind         string
		instructions string
		jsonF        bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Topos agent.",
		Long: `Create an agent on the Topos control plane.

The agent's owner and org are derived from the bearer token's claims;
they are never sent by the client. Requires the write:agents scope.`,
		Example: `  latere topos agents create --name "Build Bot" --kind worker
  latere topos agents create --name Helper --kind assistant \
    --instructions "You triage CI failures."`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || kind == "" {
				return fmt.Errorf("--name and --kind are required")
			}
			c, err := toposClient(cmd.Context(), apiURL)
			if err != nil {
				return err
			}
			var created agentDTO
			err = c.PostJSON(cmd.Context(), "/v1/agents", createAgentRequest{
				DisplayName:        name,
				Kind:               kind,
				CustomInstructions: instructions,
			}, &created)
			if err != nil {
				return err
			}
			if jsonF {
				return printJSON(created)
			}
			fmt.Fprintf(os.Stdout, "Created agent %s\n\n", created.ID) //nolint:errcheck
			printAgent(created)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	cmd.Flags().StringVar(&name, "name", "", "display name (required)")
	cmd.Flags().StringVar(&kind, "kind", "", "agent kind, e.g. assistant|worker (required)")
	cmd.Flags().StringVar(&instructions, "instructions", "", "custom system-prompt instructions")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// ---- session subgroup ----

func newToposSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Trigger and manage Topos agent sessions.",
		Long: `Trigger agent runs on the Topos control plane.

A session is one run. 'create' triggers an autonomous run and prints the
result once it completes. 'start' opens an interactive session (a coding
assistant TUI, or --print for a one-shot prompt); 'attach' reconnects to a
running session; 'ls' lists interactive sessions.`,
		Example: `  latere topos session start agent_01hxy
  latere topos session start agent_01hxy -p "summarise README.md"
  latere topos session attach sess_01hxy
  latere topos session create agent_01hxy --prompt "List the repo files."`,
	}
	cmd.AddCommand(newToposSessionCreateCmd())
	cmd.AddCommand(newToposSessionStartCmd())
	cmd.AddCommand(newToposSessionAttachCmd())
	cmd.AddCommand(newToposSessionLsCmd())
	return cmd
}

// newToposSessionCreateCmd implements `latere topos session create <agent-id>`.
func newToposSessionCreateCmd() *cobra.Command {
	var (
		apiURL string
		prompt string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:   "create <agent-id>",
		Short: "Trigger an autonomous run on an agent.",
		Long: `Trigger one autonomous run (a session) on the given agent.

POSTs the initial prompt to the agent's session endpoint; the run
executes on the control plane and the result is printed when it
completes. Requires the run:agents scope.`,
		Example: `  latere topos session create agent_01hxy --prompt "Summarise README.md"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prompt == "" {
				return fmt.Errorf("--prompt is required")
			}
			c, err := toposClient(cmd.Context(), apiURL)
			if err != nil {
				return err
			}
			var result sessionResultDTO
			err = c.PostJSON(cmd.Context(), agentPath(args[0])+"/sessions",
				sessionCreateRequest{Prompt: prompt}, &result)
			if err != nil {
				return err
			}
			if jsonF {
				return printJSON(result)
			}
			printSessionResult(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "initial prompt that starts the run (required)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func printSessionResult(r sessionResultDTO) {
	printWrappedField("session", r.SessionID)
	printWrappedField("sandbox", defaultStr(r.SandboxID, "-"))
	printWrappedField("stop_reason", defaultStr(r.StopReason, "-"))
	printWrappedField("tool_calls", fmt.Sprintf("%d", r.ToolCalls))
	printWrappedField("tokens", fmt.Sprintf("%d in / %d out", r.Usage.InputTokens, r.Usage.OutputTokens))
	if r.Output != "" {
		fprintln(os.Stdout)
		fprintln(os.Stdout, r.Output)
	}
}

// newToposAgentsListCmd implements `latere topos agents list`.
func newToposAgentsListCmd() *cobra.Command {
	var (
		apiURL string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agents visible to the current token.",
		Long:  "List the agents you can run on Topos.",
		Example: `  latere topos agents list
  latere topos agents list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := toposClient(cmd.Context(), apiURL)
			if err != nil {
				return err
			}
			var resp listAgentsResponse
			if err := c.GetJSON(cmd.Context(), "/v1/agents", &resp); err != nil {
				return err
			}
			if jsonF {
				return printJSON(resp.Agents)
			}
			if len(resp.Agents) == 0 {
				fprintln(os.Stdout, "No agents are visible to this token.")
				return nil
			}
			printAgentList(resp.Agents)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// newToposAgentsGetCmd implements `latere topos agents get <id>`.
func newToposAgentsGetCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "get <id>",
		Short:   "Get a Topos agent by id.",
		Long:    "Fetch one agent by id and print the full JSON response.",
		Example: `  latere topos agents get agent_01hxy`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := toposClient(cmd.Context(), apiURL)
			if err != nil {
				return err
			}
			var agent agentDTO
			if err := c.GetJSON(cmd.Context(), agentPath(args[0]), &agent); err != nil {
				return err
			}
			return printJSON(agent)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override the Topos API base URL")
	return cmd
}

// ---- helpers ----

// resolveToposURL returns the Topos API base URL: explicit flag wins,
// then TOPOS_API_URL env, then the public default. Passing a non-empty
// URL to api.NewClient bypasses NewClient's own env/default branch,
// which resolves SANDBOX_API_URL for Cella.
func resolveToposURL(flagURL string) string {
	if flagURL != "" {
		return flagURL
	}
	if v := os.Getenv("TOPOS_API_URL"); v != "" {
		return v
	}
	return "https://topos.latere.ai"
}

// toposClient builds an authenticated API client pointed at the Topos
// control plane. For local development, TOPOS_TOKEN overrides the saved
// token with a static bearer, so a server running with TOPOS_DEV_AUTH=true +
// TOPOS_DEV_TOKEN can be reached in one step without `latere login`.
//
// Against production, Topos validates an auth-issued, topos-audience bearer
// carrying run:agents. That is the retained auth root token (which
// `latere login` now requests run:agents and the topos audience for),
// NOT the Cella-audience token `latere cella` uses — so the Topos path uses the
// auth root token, refreshed when expired.
func toposClient(ctx context.Context, apiURL string) (*api.Client, error) {
	c := api.NewClient(resolveToposURL(apiURL))
	if v := os.Getenv("TOPOS_TOKEN"); v != "" {
		c.Token = v
		return c, nil
	}
	bearer, err := toposIdentityBearer(ctx)
	if err != nil {
		return nil, err
	}
	c.Token = bearer
	return c, nil
}

// toposIdentityBearer returns the auth-issued bearer Topos accepts: the retained
// auth root token, refreshed when within a minute of expiry. It mirrors Lux's
// authIdentityToken but is kept separate so the Topos path has its own clear
// error messages.
func toposIdentityBearer(ctx context.Context) (string, error) {
	authTok, err := api.LoadAuthToken()
	if err != nil {
		if errors.Is(err, api.ErrNoToken) {
			return "", errors.New("not signed in for Topos; run `latere login` (it grants the run:agents scope Topos needs)")
		}
		return "", err
	}
	access := authTok.AccessToken
	if authTok.RefreshToken != "" && !authTok.ExpiresAt.IsZero() &&
		time.Now().After(authTok.ExpiresAt.Add(-60*time.Second)) {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		refreshed, rerr := api.RefreshAuthToken(rctx, toposAuthBase(), authTok.RefreshToken)
		if rerr != nil {
			return "", fmt.Errorf("auth token expired and refresh failed (%w); run `latere login`", rerr)
		}
		access = refreshed.AccessToken
	}
	if access == "" {
		return "", errors.New("no auth token on file; run `latere login`")
	}
	return access, nil
}

// toposAuthBase resolves the auth service base URL for token refresh.
func toposAuthBase() string {
	if v := strings.TrimRight(os.Getenv("AUTH_URL"), "/"); v != "" {
		return v
	}
	return "https://auth.latere.ai"
}

func agentPath(id string) string {
	return "/v1/agents/" + url.PathEscape(id)
}

func printAgentList(agents []agentDTO) {
	for i, a := range agents {
		if i > 0 {
			fprintln(os.Stdout)
		}
		printAgent(a)
	}
}

func printAgent(a agentDTO) {
	printWrappedField("id", a.ID)
	printWrappedField("display_name", defaultStr(a.DisplayName, "-"))
	printWrappedField("kind", defaultStr(a.Kind, "-"))
	printWrappedField("org_id", defaultStr(a.OrgID, "-"))
	printWrappedField("owner", defaultStr(a.OwnerSub, "-"))
	if a.WorkspaceRef != "" {
		printWrappedField("workspace", a.WorkspaceRef)
	}
	if !a.CreatedAt.IsZero() {
		printWrappedField("created", humanAge(a.CreatedAt)+" ago")
	}
}
