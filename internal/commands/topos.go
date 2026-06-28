package commands

import (
	"fmt"
	"net/url"
	"os"
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
	cmd := &cobra.Command{
		Use:   "topos",
		Short: "Manage Topos agents and runs.",
		Long: `Manage agents on the Topos control plane (topos.latere.ai).

Topos is the Latere agent control plane. Use these commands to list,
inspect, and (in future releases) create and manage agent runs.

Authentication: Topos uses the same bearer token as the rest of the
Latere CLI, stored at ~/.config/latere/token.json. Run 'latere auth
login' to authenticate.

Local development: When the Topos server runs with TOPOS_DEV_AUTH=true
and TOPOS_DEV_TOKEN=<secret>, store the matching secret token via:

  latere auth login --token <secret>

The base URL defaults to https://topos.latere.ai and can be overridden
by the TOPOS_API_URL environment variable or by --api-url on any
subcommand.`,
		Example: `  latere topos agents list
  latere topos agents get agent_01hxy
  TOPOS_API_URL=http://localhost:8080 latere topos agents list`,
	}
	cmd.AddCommand(newToposAgentsCmd())
	cmd.AddCommand(newToposSessionCmd())
	return cmd
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
			c, err := toposClient(apiURL)
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
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Topos API base URL (overrides TOPOS_API_URL)")
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

A session is one autonomous run. 'create' triggers a run on an agent
with an initial prompt and prints the result once the run completes.

attach / lift / drop (interactive session control) arrive with the
lift/drop lifecycle.`,
		Example: `  latere topos session create agent_01hxy --prompt "List the repo files."`,
	}
	cmd.AddCommand(newToposSessionCreateCmd())
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
		Example: `  latere topos session create agent_01hxy --prompt "Summarise README.md"
  TOPOS_API_URL=http://localhost:8080 latere topos session create agent_01hxy -p "echo hi"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prompt == "" {
				return fmt.Errorf("--prompt is required")
			}
			c, err := toposClient(apiURL)
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
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Topos API base URL (overrides TOPOS_API_URL)")
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
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, r.Output)
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
		Long: `List agents registered on the Topos control plane.

The bearer token from ~/.config/latere/token.json is sent as the
Authorization header. The response lists agents owned by the token's
subject (or all agents for admin tokens).

For local development against a Topos server running with
TOPOS_DEV_AUTH=true and TOPOS_DEV_TOKEN=<secret>, store the matching
dev token via 'latere auth login --token <secret>', then run this
command against the local server:

  TOPOS_API_URL=http://localhost:8080 latere topos agents list`,
		Example: `  latere topos agents list
  latere topos agents list --json
  TOPOS_API_URL=http://localhost:8080 latere topos agents list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := toposClient(apiURL)
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
				fmt.Fprintln(os.Stdout, "No agents are visible to this token.")
				return nil
			}
			printAgentList(resp.Agents)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Topos API base URL (overrides TOPOS_API_URL)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// newToposAgentsGetCmd implements `latere topos agents get <id>`.
func newToposAgentsGetCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get a Topos agent by id.",
		Long:  "Fetch one agent by id and print the full JSON response.",
		Example: `  latere topos agents get agent_01hxy
  latere topos agents get agent_01hxy --api-url http://localhost:8080`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := toposClient(apiURL)
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
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Topos API base URL (overrides TOPOS_API_URL)")
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
// token file with a static bearer, so a server running with
// TOPOS_DEV_AUTH=true + TOPOS_DEV_TOKEN can be reached in one step
// without `latere auth login` (which validates against the cloud auth
// service and so rejects a local dev token).
func toposClient(apiURL string) (*api.Client, error) {
	c := api.NewClient(resolveToposURL(apiURL))
	if v := os.Getenv("TOPOS_TOKEN"); v != "" {
		c.Token = v
	}
	if err := c.MustRequireAuth(); err != nil {
		return nil, err
	}
	return c, nil
}

func agentPath(id string) string {
	return "/v1/agents/" + url.PathEscape(id)
}

func printAgentList(agents []agentDTO) {
	for i, a := range agents {
		if i > 0 {
			fmt.Fprintln(os.Stdout)
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
