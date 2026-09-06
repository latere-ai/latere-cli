// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"latere.ai/x/pkg/otel"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/latere-ai/latere-cli/internal/api"
)

// ---- DTOs (subset of evald's wire contract; keep loose so additive
//      backend changes don't break the CLI). ----

type evalApplyResultDTO struct {
	DryRun      *bool               `json:"dry_run"`
	Suite       evalApplyNounDTO    `json:"suite"`
	Tasks       []evalApplyTaskDTO  `json:"tasks"`
	Cells       evalApplyCellsDTO   `json:"cells"`
	Comparisons []evalComparisonDTO `json:"comparisons"`
	Warnings    []string            `json:"warnings,omitempty"`
}

type evalApplyNounDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // created | exists
}

type evalApplyTaskDTO struct {
	ID         string  `json:"id"`
	PromptHash string  `json:"prompt_hash"`
	Status     string  `json:"status"` // created | exists
	LineageID  *string `json:"lineage_id,omitempty"`
}

type evalApplyCellsDTO struct {
	Created   int `json:"created"`
	Exists    int `json:"exists"`
	Unmanaged int `json:"unmanaged"`
}

type evalComparisonDTO struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Outcome   string   `json:"outcome"` // created | updated
	Status    string   `json:"status"`  // single-variable | confounded(...)
	Confounds []string `json:"confounds"`
	Members   int      `json:"members"`
}

type evalSuiteDTO struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Org          string  `json:"org"`
	BudgetCapUSD float64 `json:"budget_cap_usd"`
	State        string  `json:"state"`
	CreatedAt    string  `json:"created_at"`
}

type evalCellDTO struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	Tuple  struct {
		ModelID          string `json:"model_id"`
		ModelRoute       string `json:"model_route"`
		Harness          string `json:"harness"`
		HarnessVersion   string `json:"harness_version"`
		ImageTag         string `json:"image_tag"`
		EffortConfigured string `json:"effort_configured"`
		GatewaySurface   string `json:"gateway_surface"`
	} `json:"tuple"`
	State string `json:"state"`
}

// ---- client ----

// evalClient talks to evald (eval.latere.ai) with the platform's
// static admin bearer token. Eval auth does not go through the latere
// session client, so this is plain net/http.
type evalClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// newEvalClient resolves the API base URL and token from flags first,
// then the EVAL_API_URL / EVAL_ADMIN_TOKEN environment.
func newEvalClient(apiURL, token string) (*evalClient, error) {
	if apiURL == "" {
		apiURL = os.Getenv("EVAL_API_URL")
	}
	if apiURL == "" {
		apiURL = "https://eval.latere.ai"
	}
	if token == "" {
		token = os.Getenv("EVAL_ADMIN_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("no Eval token: set EVAL_ADMIN_TOKEN or pass --token")
	}
	httpc := otel.HTTPClient()
	isDryRun := func(req *http.Request) bool {
		value := req.URL.Query().Get("dry_run")
		return value == "1" || value == "true"
	}
	httpc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := api.PreserveMethodOnRedirect(req, via); err != nil {
			return err
		}
		// Apply carries dry-run mode in the query, outside its replayed body.
		if req.Method == http.MethodPost && isDryRun(req) != isDryRun(via[0]) {
			return fmt.Errorf("redirect changed dry-run mode")
		}
		return nil
	}
	return &evalClient{
		baseURL: strings.TrimRight(apiURL, "/"),
		token:   token,
		http:    httpc,
	}, nil
}

// do issues one authenticated request and decodes the JSON response
// into out. Non-2xx responses are rendered from evald's error
// envelope {"error":{"code","message"}} as "code: message".
func (c *evalClient) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &env) == nil && env.Error.Code != "" {
			return fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return fmt.Errorf("eval API: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// doJSON issues a GET-style request and decodes the JSON response.
func (c *evalClient) doJSON(ctx context.Context, method, path string, out any) error {
	return c.do(ctx, method, path, nil, "", out)
}

// doYAML POSTs a YAML manifest body and decodes the JSON response.
func (c *evalClient) doYAML(ctx context.Context, method, path string, body []byte, out any) error {
	return c.do(ctx, method, path, bytes.NewReader(body), "application/yaml", out)
}

// ---- top-level ----

// newEvalCmd is the `latere eval …` command tree for the Eval
// platform (evald). Unlike the other product groups it authenticates
// with a static admin bearer token, not the latere session.
func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Manage Eval suites (eval.latere.ai).",
		Long: `Manage Eval suites — declarative model-evaluation matrices at
eval.latere.ai.

A suite.yaml manifest declares tasks, a model × harness matrix, and
comparisons; 'latere eval apply' reconciles it against the platform.
Authentication uses the static admin token (EVAL_ADMIN_TOKEN or
--token), not the latere session.`,
		Example: `  latere eval apply -f suite.yaml
  latere eval apply -f suite.yaml --dry-run
  latere eval suites
  latere eval cells --suite st-0001`,
	}
	cmd.AddCommand(
		newEvalApplyCmd(),
		newEvalSuitesCmd(),
		newEvalCellsCmd(),
	)
	return cmd
}

// ---- apply ----

// Matches evald's apply request body limit, including resolved prompts.
const evalMaxManifestBytes = 256 << 10

// newEvalApplyCmd registers `latere eval apply -f suite.yaml`. The
// manifest is POSTed as YAML after client-side prompt resolution:
// file:// prompt refs are inlined as prompt_text (relative to the
// manifest's directory), since the server rejects unresolved refs.
func newEvalApplyCmd() *cobra.Command {
	var (
		file   string
		dryRun bool
		apiURL string
		token  string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply a suite manifest (creates or reconciles the suite).",
		Long: `Apply a declarative suite manifest to the Eval platform.

Nonempty prompt_text takes precedence over prompt. Otherwise,
file:// prompts are read relative to the manifest's directory (the
current directory for stdin) and inlined as prompt_text before
upload; the original prompt ref is kept for provenance.
Apply never deletes: cells missing from a re-applied
manifest are reported unmanaged.

Use --dry-run to see the full reconciliation diff without writing.`,
		Example: `  latere eval apply -f suite.yaml
  latere eval apply -f suite.yaml --dry-run
  cat suite.yaml | latere eval apply -f -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("-f is required (path to a suite manifest, or - for stdin)")
			}
			body, err := readManifestBodyWithLimit(file, cmd.InOrStdin(), evalMaxManifestBytes)
			if err != nil {
				return err
			}
			baseDir := "."
			if file != "-" {
				baseDir = filepath.Dir(file)
			}
			body, err = resolvePromptRefs(body, baseDir)
			if err != nil {
				return err
			}
			c, err := newEvalClient(apiURL, token)
			if err != nil {
				return err
			}
			path := "/api/v1/suites/apply"
			if dryRun {
				path += "?dry_run=1"
			}
			var res evalApplyResultDTO
			if err := c.doYAML(cmd.Context(), http.MethodPost, path, body, &res); err != nil {
				return err
			}
			if strings.TrimSpace(res.Suite.ID) == "" || strings.TrimSpace(res.Suite.Name) == "" || strings.TrimSpace(res.Suite.Status) == "" {
				return fmt.Errorf("invalid Eval apply response: missing suite identity or status; apply outcome is unknown")
			}
			if res.DryRun == nil {
				return fmt.Errorf("invalid Eval apply response: missing dry-run mode; apply outcome is unknown")
			}
			if *res.DryRun != dryRun {
				return fmt.Errorf("invalid Eval apply response: dry_run=%t, requested %t; apply outcome is unknown", *res.DryRun, dryRun)
			}
			return printEvalApplyResult(cmd.OutOrStdout(), res)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "path to a suite manifest YAML file, or - for stdin")
	_ = cmd.MarkFlagRequired("file")
	f.BoolVar(&dryRun, "dry-run", false, "reconcile and report the diff without writing")
	f.StringVar(&apiURL, "api-url", "", "override Eval API base URL (env EVAL_API_URL)")
	f.StringVar(&token, "token", "", "Eval admin bearer token (env EVAL_ADMIN_TOKEN)")
	return cmd
}

// resolvePromptRefs inlines file:// prompt refs client-side: for each
// tasks[] entry with no nonempty prompt_text whose prompt starts with
// file://, the referenced file is read relative to baseDir and set as
// prompt_text. Existing text takes precedence, matching evald. The prompt ref
// itself is kept for provenance. The server is the authoritative
// validator, so everything else passes through untouched.
func resolvePromptRefs(manifest []byte, baseDir string) ([]byte, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(manifest))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("manifest is not valid YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("manifest is not valid YAML: %w", err)
		}
		return nil, fmt.Errorf("manifest must contain exactly one YAML document")
	}
	// Validate the full graph before following aliases, including duplicate
	// keys and recursive or excessive aliases in fields we do not edit.
	var validated map[string]any
	if err := doc.Decode(&validated); err != nil {
		return nil, fmt.Errorf("manifest is not valid YAML: %w", err)
	}
	if _, ok := validated["tasks"].([]any); !ok {
		return manifest, nil
	}
	// Decode effective prompts from the original graph before adding text.
	var parsed struct {
		Tasks evalPromptTasks `yaml:"tasks"`
	}
	if err := doc.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("manifest is not valid YAML: %w", err)
	}
	resolved := false
	remaining := evalMaxManifestBytes
	for i, entry := range parsed.Tasks.entries {
		task := entry.node
		if task == nil || entry.text != "" {
			continue
		}
		prompt := entry.prompt
		if !strings.HasPrefix(prompt, "file://") {
			continue
		}
		ref := strings.TrimPrefix(prompt, "file://")
		p := ref
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		text, err := readEvalPromptFile(p, remaining)
		if err != nil {
			return nil, fmt.Errorf("tasks[%d]: resolve prompt %q: %w", i, prompt, err)
		}
		// Keep anchored prototypes unchanged: later tasks may merge the
		// prototype while overriding prompt. Put resolved text on this task.
		original := parsed.Tasks.node.Content[i]
		updated := *original
		updated.Content = slices.Clone(original.Content)
		preserveAnchors := original.Kind == yaml.AliasNode || original.Anchor != ""
		for j := 0; j < len(original.Content); j += 2 {
			key, value := original.Content[j], original.Content[j+1]
			if key.Kind == yaml.AliasNode || (key.Value == "prompt_text" && value.Anchor != "") {
				preserveAnchors = true
			}
		}
		if preserveAnchors {
			updated = yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"}, original,
			}}
		}
		task = &updated
		parsed.Tasks.node.Content[i] = task
		value := &yaml.Node{}
		if err := value.Encode(string(text)); err != nil {
			return nil, fmt.Errorf("tasks[%d]: encode prompt: %w", i, err)
		}
		replaced := false
		for j := 0; j < len(task.Content); j += 2 {
			if task.Content[j].Value == "prompt_text" {
				task.Content[j+1] = value
				replaced = true
				break
			}
		}
		if !replaced {
			task.Content = append(task.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "prompt_text"}, value)
		}
		remaining -= len(text)
		resolved = true
	}
	if !resolved {
		return manifest, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("re-marshal manifest: %w", err)
	}
	if len(out) > evalMaxManifestBytes {
		return nil, fmt.Errorf("resolved manifest exceeds %d byte limit", evalMaxManifestBytes)
	}
	return out, nil
}

type evalPromptTasks struct {
	node    *yaml.Node
	entries []evalPromptTask
}

func (t *evalPromptTasks) UnmarshalYAML(node *yaml.Node) error {
	t.node = node
	return node.Decode(&t.entries)
}

// evalPromptTask retains the original mapping and decodes strings as evald
// does: scalar spelling (e.g. 001) is text. YAML handles merges and aliases.
type evalPromptTask struct {
	node         *yaml.Node
	prompt, text string
}

func (t *evalPromptTask) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	var values struct {
		Prompt     string `yaml:"prompt"`
		PromptText string `yaml:"prompt_text"`
	}
	if err := node.Decode(&values); err != nil {
		return err
	}
	t.node, t.prompt, t.text = node, values.Prompt, values.PromptText
	return nil
}

func readEvalPromptFile(path string, remaining int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	text, err := io.ReadAll(io.LimitReader(f, int64(remaining)+1))
	if err != nil {
		return nil, err
	}
	if len(text) > remaining {
		return nil, fmt.Errorf("resolved prompts exceed the %d byte manifest limit", evalMaxManifestBytes)
	}
	return text, nil
}

// printEvalApplyResult renders the apply diff: suite, per-task lines,
// cell counts, per-comparison lines, then warnings.
func printEvalApplyResult(dst io.Writer, res evalApplyResultDTO) error {
	var output strings.Builder
	w := &output
	if res.DryRun != nil && *res.DryRun {
		fprintln(w, "dry run — no changes written")
	}
	fprintf(w, "suite %s (%s)\n", res.Suite.Name, res.Suite.Status)
	for _, t := range res.Tasks {
		h := t.PromptHash
		if len(h) > 12 {
			h = h[:12]
		}
		line := fmt.Sprintf("  task %s %s", h, t.Status)
		if t.LineageID != nil && *t.LineageID != "" {
			line += fmt.Sprintf(" (lineage %s)", *t.LineageID)
		}
		fprintln(w, line)
	}
	fprintf(w, "cells: %d created, %d exists, %d unmanaged\n",
		res.Cells.Created, res.Cells.Exists, res.Cells.Unmanaged)
	for _, c := range res.Comparisons {
		status := c.Status
		if len(c.Confounds) > 0 && !strings.HasPrefix(status, "confounded") {
			status = fmt.Sprintf("confounded(%s)", strings.Join(c.Confounds, ", "))
		}
		fprintf(w, "  comparison %s: %s, %s, %d members\n",
			c.Name, c.Outcome, status, c.Members)
	}
	for _, warn := range res.Warnings {
		fprintf(w, "warning: %s\n", warn)
	}
	if _, err := fmt.Fprint(dst, output.String()); err != nil {
		return fmt.Errorf("write Eval apply result: %w", err)
	}
	return nil
}

// ---- suites / cells ----

func newEvalSuitesCmd() *cobra.Command {
	var (
		apiURL string
		token  string
	)
	cmd := &cobra.Command{
		Use:     "suites",
		Short:   "List Eval suites.",
		Long:    "List all suites visible to the Eval admin token.",
		Example: `  latere eval suites`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newEvalClient(apiURL, token)
			if err != nil {
				return err
			}
			var suites []evalSuiteDTO
			if err := c.doJSON(cmd.Context(), http.MethodGet, "/api/v1/suites", &suites); err != nil {
				return err
			}
			if len(suites) == 0 {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No suites."); err != nil {
					return fmt.Errorf("write Eval suites: %w", err)
				}
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fprintln(tw, "ID\tNAME\tORG\tBUDGET\tSTATE")
			for _, s := range suites {
				fprintf(tw, "%s\t%s\t%s\t$%.2f\t%s\n",
					s.ID, s.Name, s.Org, s.BudgetCapUSD, s.State)
			}
			return tw.Flush()
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override Eval API base URL (env EVAL_API_URL)")
	f.StringVar(&token, "token", "", "Eval admin bearer token (env EVAL_ADMIN_TOKEN)")
	return cmd
}

func newEvalCellsCmd() *cobra.Command {
	var (
		suite  string
		apiURL string
		token  string
	)
	cmd := &cobra.Command{
		Use:     "cells --suite <id>",
		Short:   "List a suite's cells (task × pinned tuple).",
		Long:    "List the cells of one suite: each is a task crossed with one pinned provenance tuple.",
		Example: `  latere eval cells --suite st-0001`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newEvalClient(apiURL, token)
			if err != nil {
				return err
			}
			var cells []evalCellDTO
			path := "/api/v1/cells?suite=" + url.QueryEscape(suite)
			if err := c.doJSON(cmd.Context(), http.MethodGet, path, &cells); err != nil {
				return err
			}
			if len(cells) == 0 {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No cells in this suite."); err != nil {
					return fmt.Errorf("write Eval cells: %w", err)
				}
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fprintln(tw, "MODEL\tROUTE\tHARNESS\tVERSION\tEFFORT\tSURFACE\tSTATE")
			for _, cl := range cells {
				fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					cl.Tuple.ModelID, cl.Tuple.ModelRoute, cl.Tuple.Harness,
					cl.Tuple.HarnessVersion, cl.Tuple.EffortConfigured,
					cl.Tuple.GatewaySurface, cl.State)
			}
			return tw.Flush()
		},
	}
	f := cmd.Flags()
	f.StringVar(&suite, "suite", "", "suite id to list cells for")
	_ = cmd.MarkFlagRequired("suite")
	f.StringVar(&apiURL, "api-url", "", "override Eval API base URL (env EVAL_API_URL)")
	f.StringVar(&token, "token", "", "Eval admin bearer token (env EVAL_ADMIN_TOKEN)")
	return cmd
}
