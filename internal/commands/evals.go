// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"math"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"latere.ai/x/sdk-go/agents"
	"latere.ai/x/sdk-go/evals"
	"latere.ai/x/sdk-go/latere"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The evals commands reach Evals, the platform's capability for experiments
// on agents and on programs built on models, which the Examen core serves
// under the platform origin (spec 015), through the Latere Go SDK's evals
// package, which carries the routes, the token, the retries and the API's
// errors. The address a person gives includes the path the origin serves
// Evals under, evals.BasePath, and the commands cut it off to name the
// origin.

// defaultEvalsURL is the Evals API at the platform origin, its base path
// included.
const defaultEvalsURL = latere.DefaultBaseURL + evals.BasePath

// evalsAudience is the aud claim of the bearer every evals command
// presents: the Examen core's own audience, which auth mints actor tokens
// for on the latere-cli client. The URL selects the deployment; the
// audience is what the issuer stamps.
const evalsAudience = "examen"

// evalsURLUsage is the --api-url help every evals command shares.
const evalsURLUsage = "Evals API base URL, including its /v1/evals path (default " + defaultEvalsURL + ", or $" + envEvalsURL + ")"

// evalsTimeout bounds one request, its retries included. Every read the
// commands make is one page or one object.
const evalsTimeout = 30 * time.Second

// evalsPage is the page size a list asks for: the API's largest.
const evalsPage = evals.MaxLimit

// evalsReads bounds how many definitions a list reads at once to name
// what its rows name by digest.
const evalsReads = 8

// experimentNameKey is the metadata key an experiment's name is kept
// under. Examen keeps no name of its own on an experiment, so the console
// keeps the name a person gives one at its start here, and an experiment
// created through the API carries it the same way.
const experimentNameKey = "name"

// evalsStreamFloor is how long a run of the progress stream's own
// reconnects must last, when it read no frame, for the watch to connect
// again rather than give up. The API closes a stream at its bearer's
// expiry, and the SDK mints a bearer of at least half a minute, so a quiet
// experiment's stream lasts far longer; one that closes faster is refusing
// to serve. A variable so a test's stub can close at once.
var evalsStreamFloor = 10 * time.Second

// evalsFailed is a result that is a failure, for exit code 1.
func evalsFailed(format string, a ...any) error {
	return &exitError{code: exitFailure, err: fmt.Errorf(format, a...)}
}

func newEvalsCmd() *cobra.Command {
	var apiURL, authURL string
	cmd := &cobra.Command{
		Use:   "evals",
		Short: "Read your Evals definitions and experiments, and follow an experiment.",
		Long: `Read what you keep in Evals, the platform's experiments on agents and on
programs built on models: the suites, tasks, metrics, graders and
candidates of your current context, and the experiments run on them.

  latere evals suites list               every suite, its current version
  latere evals suites show <ref>         one version and its fields
  latere evals experiments list          experiments, newest first
  latere evals experiments show <id>     one experiment and its progress
  latere evals experiments watch <id>    follow one to its end

The console at https://platform.latere.ai/console/evals creates and runs
experiments and shows their results, comparisons, runs and the files each
run made; 'experiments show' prints the address of each.

A <ref> is a name, which reads its current version, a name@digest, a
digest, or an id. 'latere org' switches the context.

The commands call https://api.latere.ai/v1/evals with a token minted for
your login. LATERE_EVALS_URL or --api-url overrides the address, and
LATERE_EVALS_TOKEN presents a bearer as given, such as one exchanged from
a key. Each command exits 0 when it is done, 1 for a result that is a
failure, 2 when the request was refused and running it again gives the
same answer, and 3 when no answer came and running it again may succeed.`,
		Example: `  latere evals suites list
  latere evals candidates show acme-harness-1-4
  latere evals experiments list --state running
  latere evals experiments show exp_01jb2c3d4e5f6g7h8j9k0m1n2p
  latere evals experiments watch exp_01jb2c3d4e5f6g7h8j9k0m1n2p --timeout 6h`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&apiURL, "api-url", "", evalsURLUsage)
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the API URL)")
	for _, kind := range evalsKinds {
		cmd.AddCommand(newEvalsKindCmd(kind, &apiURL, &authURL))
	}
	cmd.AddCommand(newEvalsExperimentsCmd(&apiURL, &authURL))
	return cmd
}

// ---- the client ----

// evalsClient is one command's connection to the Evals API: the SDK's
// client, the address as the command was given it, which messages name,
// and the console's origin, which links name.
type evalsClient struct {
	ev      *evals.Client
	base    string
	console string

	// origin and authURL are what the agents client is built from, once a
	// candidate names an agent version to read.
	origin, authURL string
	// versions are the agent candidates whose agent versions were read,
	// by candidate digest: the agent, its version, its model's short name
	// and its reasoning level.
	versions map[string]agentVersion
	// unnamed is why agent candidates kept their definitions' names when
	// the agents API could not be read, such as a token for Evals alone;
	// nil when it could, or when it answered that the agent is not one
	// the caller may read.
	unnamed error
}

// newEvalsClient resolves the API's address, the flag, then
// $LATERE_EVALS_URL, then the platform origin, and its bearer:
// LATERE_EVALS_TOKEN as given, else actor tokens minted for evalsAudience
// from the saved login and minted again as they lapse, so the login token
// never reaches the API and a long watch keeps working.
func newEvalsClient(flagURL, authURL string) (*evalsClient, error) {
	base := strings.TrimRight(cmp.Or(flagURL, os.Getenv(envEvalsURL), defaultEvalsURL), "/")
	origin, ok := strings.CutSuffix(base, evals.BasePath)
	if !ok {
		return nil, &exitError{code: exitRefused, err: fmt.Errorf("%s is not an Evals API base URL: it ends in %s, as %s does", base, evals.BasePath, defaultEvalsURL)}
	}
	cfg, err := api.Product{
		BaseURL: origin, AuthURL: authURL, Audience: evalsAudience,
		Token: strings.TrimSpace(os.Getenv(envEvalsToken)), Timeout: evalsTimeout,
	}.Config()
	if err != nil {
		return nil, &exitError{code: exitRefused, err: err}
	}
	return &evalsClient{ev: evals.New(cfg), base: base, console: resolvePlatformURL(""), origin: origin, authURL: authURL}, nil
}

// raw is a model of the Evals API that keeps the JSON it was answered with,
// which --json prints.
type raw interface{ RawJSON() json.RawMessage }

// pages reads a list one page at a time from cursor "": its first page, or
// with all every page. more reports a page the read left unread.
func pages[T raw](ctx context.Context, c *evalsClient, all bool, page func(ctx context.Context, cursor string) (*latere.Page[T], error)) (items []T, more bool, err error) {
	items = []T{}
	cursor := ""
	for {
		p, err := page(ctx, cursor)
		if err != nil {
			return nil, false, c.refused(err)
		}
		items = append(items, p.Items...)
		switch {
		case p.NextCursor == "":
			return items, false, nil
		case !all:
			return items, true, nil
		}
		cursor = p.NextCursor
	}
}

// every reads every item of a list, as one of the SDK's iterators yields
// them.
func every[T any](c *evalsClient, seq iter.Seq2[T, error]) ([]T, error) {
	items := []T{}
	for item, err := range seq {
		if err != nil {
			return nil, c.refused(err)
		}
		items = append(items, item)
	}
	return items, nil
}

// raws are the items' JSON as the API answered them.
func raws[T raw](items []T) []json.RawMessage {
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		out[i] = item.RawJSON()
	}
	return out
}

// refused is what a command says of a call that failed, with its exit
// code, as apiRefused words it for Evals.
func (c *evalsClient) refused(err error) error {
	return apiRefused("Evals", c.base, err)
}

// experimentLink is the console's page of an experiment.
func (c *evalsClient) experimentLink(id string) string {
	return c.console + "/console/evals/experiments/" + url.PathEscape(id)
}

// ---- the definitions ----

// evalsRecordFields are the members of a version's answer that are its
// record rather than its kind's fields.
var evalsRecordFields = []string{"id", "owner", "name", "digest", "current", "created_at"}

// evalsRow is one version of a definition as a list shows it: its record,
// its kind's key columns, and the API's answer, which --json prints.
type evalsRow struct {
	evals.Version
	key string
	raw json.RawMessage
}

// RawJSON is the version as the API answered it.
func (r evalsRow) RawJSON() json.RawMessage { return r.raw }

// evalsKind is a kind of definition the API keeps in versions, how its
// list shows it, and the SDK's calls that read it.
type evalsKind struct {
	// path is the API's collection and the command's word, such as suites.
	path string
	// one names a single object in help and messages.
	one string
	// column is the header of the list's key columns, which evalsRow.key
	// fills.
	column string
	// list reads a page of the current version of each name, and versions
	// a page of every version of one name, each from cursor; get reads the
	// version a ref names, as the API answered it.
	list     func(ctx context.Context, ev *evals.Client, cursor string) (*latere.Page[evalsRow], error)
	versions func(ctx context.Context, ev *evals.Client, name, cursor string) (*latere.Page[evalsRow], error)
	get      func(ctx context.Context, ev *evals.Client, ref string) (json.RawMessage, error)
}

// evalsRows is a page of definitions as rows, row giving each one's record
// and key columns.
func evalsRows[P raw](page *latere.Page[P], err error, row func(P) (evals.Version, string)) (*latere.Page[evalsRow], error) {
	if err != nil {
		return nil, err
	}
	out := &latere.Page[evalsRow]{Items: make([]evalsRow, len(page.Items)), NextCursor: page.NextCursor}
	for i, item := range page.Items {
		v, key := row(item)
		out.Items[i] = evalsRow{Version: v, key: key, raw: item.RawJSON()}
	}
	return out, nil
}

// rawOf is a definition as the API answered it.
func rawOf[P raw](v P, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return v.RawJSON(), nil
}

// The key columns of each kind of definition.
func metricRow(m *evals.Metric) (evals.Version, string) {
	return m.Version, dash(string(m.Type)) + "\t" + dash(string(m.Level))
}

func graderRow(g *evals.Grader) (evals.Version, string) {
	return g.Version, dash(string(g.Kind)) + "\t" + dash(string(g.Level))
}

func taskRow(t *evals.Task) (evals.Version, string) {
	return t.Version, strconv.Itoa(len(t.Checks))
}

func suiteRow(s *evals.Suite) (evals.Version, string) {
	return s.Version, strconv.Itoa(len(s.Tasks)) + "\t" + dash(shortRef(s.PrimaryMetric))
}

func candidateRow(c *evals.Candidate) (evals.Version, string) {
	return c.Version, dash(string(c.Kind)) + "\t" + dash(candidateRuns(c))
}

// listParams is the page a list of definitions asks for.
func listParams(cursor string) *evals.ListVersionsParams {
	return &evals.ListVersionsParams{Limit: evalsPage, Cursor: cursor}
}

// evalsKinds are the definitions, in the order an experiment is built from
// them.
var evalsKinds = []evalsKind{
	{
		path: "metrics", one: "metric", column: "TYPE\tLEVEL",
		list: func(ctx context.Context, ev *evals.Client, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListMetricsPage(ctx, &evals.ListMetricsParams{Limit: evalsPage, Cursor: cursor})
			return evalsRows(p, err, metricRow)
		},
		versions: func(ctx context.Context, ev *evals.Client, name, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListMetricVersionsPage(ctx, name, listParams(cursor))
			return evalsRows(p, err, metricRow)
		},
		get: func(ctx context.Context, ev *evals.Client, ref string) (json.RawMessage, error) {
			return rawOf(ev.GetMetric(ctx, ref))
		},
	},
	{
		path: "graders", one: "grader", column: "KIND\tLEVEL",
		list: func(ctx context.Context, ev *evals.Client, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListGradersPage(ctx, &evals.ListGradersParams{Limit: evalsPage, Cursor: cursor})
			return evalsRows(p, err, graderRow)
		},
		versions: func(ctx context.Context, ev *evals.Client, name, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListGraderVersionsPage(ctx, name, listParams(cursor))
			return evalsRows(p, err, graderRow)
		},
		get: func(ctx context.Context, ev *evals.Client, ref string) (json.RawMessage, error) {
			return rawOf(ev.GetGrader(ctx, ref))
		},
	},
	{
		path: "tasks", one: "task", column: "CHECKS",
		list: func(ctx context.Context, ev *evals.Client, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListTasksPage(ctx, &evals.ListTasksParams{Limit: evalsPage, Cursor: cursor})
			return evalsRows(p, err, taskRow)
		},
		versions: func(ctx context.Context, ev *evals.Client, name, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListTaskVersionsPage(ctx, name, listParams(cursor))
			return evalsRows(p, err, taskRow)
		},
		get: func(ctx context.Context, ev *evals.Client, ref string) (json.RawMessage, error) {
			return rawOf(ev.GetTask(ctx, ref))
		},
	},
	{
		path: "suites", one: "suite", column: "TASKS\tPRIMARY METRIC",
		list: func(ctx context.Context, ev *evals.Client, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListSuitesPage(ctx, &evals.ListSuitesParams{Limit: evalsPage, Cursor: cursor})
			return evalsRows(p, err, suiteRow)
		},
		versions: func(ctx context.Context, ev *evals.Client, name, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListSuiteVersionsPage(ctx, name, listParams(cursor))
			return evalsRows(p, err, suiteRow)
		},
		get: func(ctx context.Context, ev *evals.Client, ref string) (json.RawMessage, error) {
			return rawOf(ev.GetSuite(ctx, ref))
		},
	},
	{
		path: "candidates", one: "candidate", column: "KIND\tRUNS",
		list: func(ctx context.Context, ev *evals.Client, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListCandidatesPage(ctx, &evals.ListCandidatesParams{Limit: evalsPage, Cursor: cursor})
			return evalsRows(p, err, candidateRow)
		},
		versions: func(ctx context.Context, ev *evals.Client, name, cursor string) (*latere.Page[evalsRow], error) {
			p, err := ev.ListCandidateVersionsPage(ctx, name, listParams(cursor))
			return evalsRows(p, err, candidateRow)
		},
		get: func(ctx context.Context, ev *evals.Client, ref string) (json.RawMessage, error) {
			return rawOf(ev.GetCandidate(ctx, ref))
		},
	},
}

// candidateRuns is what a candidate runs, as its kind declares it.
func candidateRuns(c *evals.Candidate) string {
	switch {
	case c.Agent != nil:
		return strings.TrimSpace(shortRef(c.Agent.AgentVersion) + " " + c.Agent.Model)
	case c.Program != nil:
		return c.Program.Image
	case c.External != nil:
		return strings.TrimSpace(c.External.Harness + " " + c.External.Version)
	}
	return ""
}

func newEvalsKindCmd(kind evalsKind, apiURL, authURL *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   kind.path,
		Short: "List and show your " + kind.path + ".",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newEvalsKindListCmd(kind, apiURL, authURL), newEvalsKindShowCmd(kind, apiURL, authURL))
	return cmd
}

func newEvalsKindListCmd(kind evalsKind, apiURL, authURL *string) *cobra.Command {
	var all, jsonF, quiet bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the " + kind.path + " of your current context.",
		Long: "List the " + kind.path + ` of your current context, newest first: the current
version of each name, with its digest and when it was made. The first page
holds up to 200; --all reads every page.

--json prints the versions as the API answers them, and -q prints each one
as name@digest, the reference that names exactly that version.`,
		Example: "  latere evals " + kind.path + " list\n  latere evals " + kind.path + " list --all -q",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newEvalsClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			rows, more, err := pages(cmd.Context(), c, all, func(ctx context.Context, cursor string) (*latere.Page[evalsRow], error) {
				return kind.list(ctx, c.ev, cursor)
			})
			if err != nil {
				return err
			}
			if jsonF {
				return printJSON(cmd.OutOrStdout(), raws(rows))
			}
			printVersions(cmd.OutOrStdout(), kind, rows, quiet)
			if more {
				fprintln(cmd.ErrOrStderr(), "More on the next page; --all reads every page.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "read every page")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print each version as name@digest alone")
	return cmd
}

func printVersions(out io.Writer, kind evalsKind, versions []evalsRow, quiet bool) {
	if quiet {
		for _, v := range versions {
			fprintln(out, stripANSI(v.Name+"@"+v.Digest))
		}
		return
	}
	if len(versions) == 0 {
		fprintf(out, "No %s in your current context.\n", kind.path)
		return
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fprintf(w, "NAME\tDIGEST\t%s\tUPDATED\n", kind.column)
	for _, v := range versions {
		fprintf(w, "%s\t%s\t%s\t%s\n", stripANSI(v.Name), shortDigest(v.Digest), stripANSI(v.key), humanAge(v.CreatedAt))
	}
	if err := w.Flush(); err != nil {
		fprintln(out, err)
	}
}

func newEvalsKindShowCmd(kind evalsKind, apiURL, authURL *string) *cobra.Command {
	var versionsF, jsonF bool
	cmd := &cobra.Command{
		Use:   "show <ref>",
		Short: "Show one " + kind.one + " version and its fields.",
		Long: "Show one " + kind.one + ` version: its name, digest and id, whether it is the
current one, when it was made, and its fields as applied, in YAML. The
<ref> is a name, which reads its current version, a name@digest, a digest,
or an id.

--versions lists every version of the name instead, newest first, the
current one marked. --json prints the API's answer.`,
		Example: "  latere evals " + kind.path + " show <name>\n  latere evals " + kind.path + " show <name> --versions",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newEvalsClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if versionsF {
				name, _, _ := strings.Cut(args[0], "@")
				rows, _, err := pages(cmd.Context(), c, true, func(ctx context.Context, cursor string) (*latere.Page[evalsRow], error) {
					return kind.versions(ctx, c.ev, name, cursor)
				})
				if err != nil {
					return err
				}
				if jsonF {
					return printJSON(out, raws(rows))
				}
				printVersionHistory(out, rows)
				return nil
			}
			raw, err := kind.get(cmd.Context(), c.ev, args[0])
			if err != nil {
				return c.refused(err)
			}
			if jsonF {
				return printJSON(out, raw)
			}
			return printVersion(out, raw)
		},
	}
	cmd.Flags().BoolVar(&versionsF, "versions", false, "list every version of the name")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// printVersion prints a version's record as fields, then the kind's own
// fields as YAML, the form a definition is written in.
func printVersion(out io.Writer, raw json.RawMessage) error {
	var v evals.Version
	if err := json.Unmarshal(raw, &v); err != nil {
		return &exitError{code: exitNoAnswer, err: fmt.Errorf("read the Evals API's answer: %w", err)}
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &exitError{code: exitNoAnswer, err: fmt.Errorf("read the Evals API's answer: %w", err)}
	}
	for _, k := range evalsRecordFields {
		delete(fields, k)
	}
	current := "no"
	if v.Current {
		current = "yes"
	}
	var b strings.Builder
	for _, f := range [][2]string{
		{"name", v.Name}, {"digest", v.Digest}, {"id", v.ID}, {"owner", v.Owner},
		{"current", current}, {"created", formatTime(v.CreatedAt)},
	} {
		b.WriteString(formatWrappedField(f[0], stripANSI(f[1])))
	}
	if len(fields) > 0 {
		var body bytes.Buffer
		enc := yaml.NewEncoder(&body)
		enc.SetIndent(2)
		if err := enc.Encode(fields); err != nil {
			return fmt.Errorf("render the fields as YAML: %w", err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("render the fields as YAML: %w", err)
		}
		b.WriteString("\n" + stripANSI(body.String()))
	}
	fprint(out, b.String())
	return nil
}

func printVersionHistory(out io.Writer, versions []evalsRow) {
	if len(versions) == 0 {
		fprintln(out, "No versions.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fprintln(w, "DIGEST\tID\tCURRENT\tCREATED")
	for _, v := range versions {
		current := ""
		if v.Current {
			current = "current"
		}
		fprintf(w, "%s\t%s\t%s\t%s\n", stripANSI(v.Digest), stripANSI(v.ID), current, formatTime(v.CreatedAt))
	}
	if err := w.Flush(); err != nil {
		fprintln(out, err)
	}
}

// ---- experiments ----

// evalsRetryableStates are the states a parked cell waits in for a retry,
// the ones the API's cell retry resumes. A canceled cell is parked too,
// but its experiment was canceled, so it is not listed one by one.
var evalsRetryableStates = []evals.CellState{evals.CellBlocked, evals.CellOverBudget}

// evalsParkedStates are the states of a cell that will not finish on its
// own: the retryable ones and canceled.
var evalsParkedStates = append(slices.Clone(evalsRetryableStates), evals.CellCanceled)

func newEvalsExperimentsCmd(apiURL, authURL *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "experiments",
		Short: "List, show and follow experiments.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newEvalsExperimentsListCmd(apiURL, authURL),
		newEvalsExperimentsShowCmd(apiURL, authURL),
		newEvalsExperimentsWatchCmd(apiURL, authURL),
	)
	return cmd
}

func newEvalsExperimentsListCmd(apiURL, authURL *string) *cobra.Command {
	var suite, state, mode string
	var all, jsonF, quiet bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the experiments of your current context.",
		Long: `List the experiments of your current context, newest first: the id, the
name it was given, its suite by name, how many candidates, the state, the
mode, the runs done of those there are, and when it started. The first
page holds up to 200; --all reads every page.

--suite takes a suite's name, for every version of it, or its digest.
--state and --mode keep the experiments in that state or mode. --json
prints the experiments as the API answers them, and -q their ids alone.`,
		Example: `  latere evals experiments list
  latere evals experiments list --suite fix-flaky-tests --state complete
  latere evals experiments list -q`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newEvalsClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			experiments, more, err := pages(cmd.Context(), c, all, func(ctx context.Context, cursor string) (*latere.Page[*evals.Experiment], error) {
				return c.ev.ListExperimentsPage(ctx, &evals.ListExperimentsParams{
					Suite: suite, State: evals.ExperimentState(state), Mode: evals.Mode(mode),
					Limit: evalsPage, Cursor: cursor,
				})
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, raws(experiments))
			}
			if quiet {
				printExperiments(out, experiments, nil, true)
				return nil
			}
			suites, err := c.suiteNames(cmd.Context(), experiments)
			if err != nil {
				return err
			}
			printExperiments(out, experiments, suites, false)
			if more {
				fprintln(cmd.ErrOrStderr(), "More on the next page; --all reads every page.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&suite, "suite", "", "a suite's name or digest")
	cmd.Flags().StringVar(&state, "state", "", "keep the experiments in this state")
	cmd.Flags().StringVar(&mode, "mode", "", "keep the experiments in this mode: execute or import")
	cmd.Flags().BoolVar(&all, "all", false, "read every page")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print the ids alone")
	return cmd
}

// suiteNames reads the name of each suite the experiments name by digest,
// each once and evalsReads at a time, and answers each digest's name. A
// suite the caller may not read, such as another owner's, reads as missing
// and has no entry.
func (c *evalsClient) suiteNames(ctx context.Context, experiments []*evals.Experiment) (map[string]string, error) {
	digests := map[string]bool{}
	for _, e := range experiments {
		if strings.HasPrefix(e.Suite, "sha256:") {
			digests[e.Suite] = true
		}
	}
	names := make(map[string]string, len(digests))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(evalsReads)
	for digest := range digests {
		g.Go(func() error {
			v, err := c.ev.GetSuite(gctx, digest)
			switch {
			case errors.Is(err, latere.ErrNotFound):
				return nil
			case err != nil:
				return c.refused(err)
			}
			mu.Lock()
			names[digest] = v.Name
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return names, nil
}

// experimentName is the name an experiment was given, or "" where it has
// none.
func experimentName(e *evals.Experiment) string {
	return strings.TrimSpace(e.Metadata[experimentNameKey])
}

// printExperiments prints the experiments as a table, each suite by the
// name suites gives its digest, or by its digest where suites has none; or
// their ids alone when quiet.
func printExperiments(out io.Writer, experiments []*evals.Experiment, suites map[string]string, quiet bool) {
	if quiet {
		for _, e := range experiments {
			fprintln(out, stripANSI(e.ID))
		}
		return
	}
	if len(experiments) == 0 {
		fprintln(out, "No experiments in your current context.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fprintln(w, "ID\tNAME\tSUITE\tCANDIDATES\tSTATE\tMODE\tRUNS\tSTARTED")
	for _, e := range experiments {
		suite := cmp.Or(suites[e.Suite], shortRef(e.Suite))
		fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", stripANSI(e.ID), dash(stripANSI(oneLine(experimentName(e)))), stripANSI(suite), len(e.Candidates),
			stripANSI(string(e.State)), stripANSI(string(e.Mode)), runsDone(e), humanAge(e.CreatedAt))
	}
	if err := w.Flush(); err != nil {
		fprintln(out, err)
	}
}

// runsDone is the runs an experiment has finished, "<done>/<declared>" for
// an execute experiment and the count alone for an import, which declares
// none; "-" when nothing is declared and nothing ran. A run a retry
// replaced counts once, as the console counts it.
func runsDone(e *evals.Experiment) string {
	done, declared := e.RunsDone(), e.RunsDeclared()
	switch {
	case declared > 0:
		return fmt.Sprintf("%d/%d", done, declared)
	case done > 0:
		return strconv.Itoa(done)
	}
	return "-"
}

func newEvalsExperimentsShowCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show an experiment, its progress and its comparisons.",
		Long: `Show one experiment: the name it was given, its suite and candidates by
name, its state and mode, the trials per cell and how many run at once, its spending cap with what it
spent and has left, who started it and when.

Then its progress: its cells by state, the runs done of those declared
(a run a retry replaced counts once), its trials by state, and its finished trials
by outcome, such as completed, timed_out or infra_failed. A cell parked
blocked or over_budget is listed with its reason. Last, each comparison:
what it compares, whether it is computed, and whether its figures are
final or still provisional. The console's address of the experiment, which
the command prints, shows each comparison's figures.

--json prints the experiment as the API answers it.`,
		Example: `  latere evals experiments show exp_01jb2c3d4e5f6g7h8j9k0m1n2p`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newEvalsClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			e, err := c.ev.GetExperiment(cmd.Context(), args[0])
			if err != nil {
				return c.refused(err)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, e.RawJSON())
			}
			parked, err := c.parkedCells(cmd.Context(), e)
			if err != nil {
				return err
			}
			comparisons, err := every(c, c.ev.ListExperimentComparisons(cmd.Context(), e.ID, &evals.ListExperimentComparisonsParams{Limit: evalsPage}))
			if err != nil {
				return err
			}
			names, err := c.names(cmd.Context(), e, parked)
			if err != nil {
				return err
			}
			if c.unnamed != nil {
				fprintln(cmd.ErrOrStderr(), "Agent candidates are named by their definitions: their agents' versions could not be read: "+apiErrText(c.unnamed))
			}
			fprint(out, formatExperiment(e, parked, comparisons, names, c.versions, c.experimentLink(e.ID)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// parkedCells reads the cells of e that wait for a retry, the states its
// progress counts any in alone.
func (c *evalsClient) parkedCells(ctx context.Context, e *evals.Experiment) ([]*evals.Cell, error) {
	var cells []*evals.Cell
	for _, state := range evalsRetryableStates {
		if e.Progress.Cells[state] == 0 {
			continue
		}
		some, err := every(c, c.ev.ListCells(ctx, e.ID, &evals.ListCellsParams{State: state, Limit: evalsPage}))
		if err != nil {
			return nil, err
		}
		cells = append(cells, some...)
	}
	return cells, nil
}

// names reads the name of each definition an experiment names by digest
// alone, its suite, its candidates and the tasks of its parked cells, and
// answers what each digest is printed as: its name, or name@ and its
// short digest where two of the digests share a name, such as two versions
// of one candidate. A definition the caller may not read, such as another
// owner's suite, reads as missing and is printed by its short digest.
func (c *evalsClient) names(ctx context.Context, e *evals.Experiment, parked []*evals.Cell) (map[string]string, error) {
	names := map[string]string{}
	// name reads the name of the version of a kind of definition a
	// digest names.
	type name func(ctx context.Context, digest string) (string, error)
	suite := func(ctx context.Context, digest string) (string, error) {
		v, err := c.ev.GetSuite(ctx, digest)
		if err != nil {
			return "", err
		}
		return v.Name, nil
	}
	// topos holds the agent each agent candidate runs, by digest.
	topos := map[string]*evals.AgentCandidate{}
	candidate := func(ctx context.Context, digest string) (string, error) {
		v, err := c.ev.GetCandidate(ctx, digest)
		if err != nil {
			return "", err
		}
		if v.Agent != nil {
			topos[digest] = v.Agent
		}
		return v.Name, nil
	}
	task := func(ctx context.Context, digest string) (string, error) {
		v, err := c.ev.GetTask(ctx, digest)
		if err != nil {
			return "", err
		}
		return v.Name, nil
	}
	add := func(read name, ref string) error {
		if _, done := names[ref]; done || !strings.HasPrefix(ref, "sha256:") {
			return nil
		}
		n, err := read(ctx, ref)
		switch {
		case errors.Is(err, latere.ErrNotFound):
			names[ref] = ""
		case err != nil:
			return c.refused(err)
		default:
			names[ref] = n
		}
		return nil
	}
	type lookup struct {
		read name
		ref  string
	}
	refs := []lookup{{suite, e.Suite}}
	for _, cand := range append(slices.Clone(e.Candidates), e.Reference.Candidate) {
		refs = append(refs, lookup{candidate, cand})
	}
	for _, cell := range parked {
		refs = append(refs, lookup{task, cell.Task}, lookup{candidate, cell.Candidate})
	}
	for _, r := range refs {
		if err := add(r.read, r.ref); err != nil {
			return nil, err
		}
	}
	if err := c.agentLabels(ctx, names, topos); err != nil {
		return nil, err
	}
	held := map[string]int{}
	for _, n := range names {
		held[n]++
	}
	for ref, n := range names {
		switch {
		case n == "":
			names[ref] = shortDigest(ref)
		case held[n] > 1:
			names[ref] = n + "@" + shortDigest(ref)
		}
	}
	return names, nil
}

// agentLabels renames the agent candidates in names the way the console
// does, "<agent> v<N> · <model>": the agent and the version of it the
// candidate runs, and the model it runs on past its vendor and a leading
// "claude-". Where two labels read the same, the reasoning level is added
// when it tells them apart; otherwise the candidates keep their
// definitions' names. A candidate whose agent versions cannot be read, an
// agent that is missing or another owner's, keeps its definition's name.
// The names are a reading aid, so the agents API failing for any other
// reason, a token for Evals alone among them, keeps them too and records
// why in c.unnamed for the command to say; only the command's own context
// ending fails it.
func (c *evalsClient) agentLabels(ctx context.Context, names map[string]string, topos map[string]*evals.AgentCandidate) error {
	// versions are the digests of each agent's versions, by number, read
	// once per agent; nil where they could not be read.
	versions := map[string]map[string]int{}
	var ag *agents.Client
	read := func(agent string) (map[string]int, error) {
		if v, done := versions[agent]; done || c.unnamed != nil {
			return v, nil
		}
		if ag == nil {
			a, err := newAgentsClient(cmp.Or(os.Getenv(envAgentsURL), c.origin+agents.BasePath), c.authURL)
			if err != nil {
				c.unnamed = err
				return nil, nil
			}
			ag = a.ag
		}
		byDigest := map[string]int{}
		for v, err := range ag.ListAgentVersions(ctx, agent, &agents.ListAgentVersionsParams{Limit: evalsPage}) {
			switch {
			case err == nil:
			case ctx.Err() != nil:
				return nil, ctx.Err()
			case errors.Is(err, latere.ErrNotFound), errors.Is(err, latere.ErrForbidden):
				versions[agent] = nil
				return nil, nil
			default:
				c.unnamed = err
				return nil, nil
			}
			byDigest[v.Digest] = v.Version
		}
		versions[agent] = byDigest
		return byDigest, nil
	}
	type labeled struct{ ref, label, reasoning string }
	var found []labeled
	for _, ref := range slices.Sorted(maps.Keys(topos)) {
		a := topos[ref]
		i := strings.LastIndex(a.AgentVersion, "@")
		if i <= 0 {
			continue
		}
		agent, digest := a.AgentVersion[:i], a.AgentVersion[i+1:]
		byDigest, err := read(agent)
		if err != nil {
			return err
		}
		n, ok := byDigest[digest]
		if !ok {
			continue
		}
		label := fmt.Sprintf("%s v%d", agent, n)
		if model := shortModel(a.Model); model != "" {
			label += " · " + model
		}
		if c.versions == nil {
			c.versions = map[string]agentVersion{}
		}
		c.versions[ref] = agentVersion{agent: agent, n: n, model: shortModel(a.Model), reasoning: a.Reasoning}
		found = append(found, labeled{ref, label, a.Reasoning})
	}
	byLabel := map[string][]labeled{}
	for _, l := range found {
		byLabel[l.label] = append(byLabel[l.label], l)
	}
	for label, group := range byLabel {
		if len(group) == 1 {
			names[group[0].ref] = label
			continue
		}
		levels := map[string]bool{}
		for _, l := range group {
			levels[l.reasoning] = true
		}
		if len(levels) < len(group) {
			continue
		}
		for _, l := range group {
			names[l.ref] = label
			if l.reasoning != "" {
				names[l.ref] += " · " + l.reasoning + " reasoning"
			}
		}
	}
	return nil
}

// agentVersion is an agent candidate as the version of its agent it runs.
type agentVersion struct {
	agent, model, reasoning string
	n                       int
}

// tupleFields are the fields of a run's setup a comparison names among its
// confounds when its two sides differ in them, as the console reads them.
var tupleFields = []string{"candidate.kind", "agent_or_image_digest", "program", "model", "reasoning", "tools", "task_hash", "environment_digest", "workspace_ref", "grader_digests"}

// versionFields are the setup fields a version of an agent carries, or a
// candidate sets beside it: two versions of one agent that also differ in
// these are compared as versions as a whole.
var versionFields = []string{"candidate.kind", "agent_or_image_digest", "program", "model", "reasoning", "tools"}

// versionPair reads a comparison of two versions of one agent as the
// console does: what varies, "goalless-agent v1 → v2 (model haiku-4.5 →
// haiku-5.5)", and its confounds without the settings that change with the
// version. ok is false for any other comparison, which keeps the API's
// variable and confounds.
func versionPair(c *evals.Comparison, versions map[string]agentVersion) (varies string, confounds []string, ok bool) {
	a, okA := versions[c.Candidate]
	b, okB := versions[c.ReferenceCandidate]
	if !okA || !okB || a.agent != b.agent || a.n == b.n {
		return "", nil, false
	}
	differs := slices.DeleteFunc(slices.Clone(c.Confounds), func(f string) bool { return !slices.Contains(tupleFields, f) || slices.Contains(c.Variable, f) })
	if len(differs) == 0 {
		differs = c.Variable
	}
	for _, f := range differs {
		if !slices.Contains(versionFields, f) {
			return "", nil, false
		}
	}
	// The reference is the earlier side the change is read from.
	varies = fmt.Sprintf("%s v%d → v%d", a.agent, b.n, a.n)
	var also []string
	if a.model != b.model {
		also = append(also, "model "+b.model+" → "+a.model)
	}
	if a.reasoning != b.reasoning {
		also = append(also, "reasoning "+cmp.Or(b.reasoning, "default")+" → "+cmp.Or(a.reasoning, "default"))
	}
	if len(also) > 0 {
		varies += " (" + strings.Join(also, ", ") + ")"
	}
	confounds = slices.DeleteFunc(slices.Clone(c.Confounds), func(f string) bool { return slices.Contains(versionFields, f) })
	return varies, confounds, true
}

// apiErrText is the sentence of an error from an API: its refusal's text
// with its status, or the error itself for one that is no refusal.
func apiErrText(err error) string {
	var e *latere.Error
	if errors.As(err, &e) {
		return fmt.Sprintf("status %d: %s", e.StatusCode, api.RefusalText(e))
	}
	return err.Error()
}

// shortModel is a model's id past its vendor and a leading "claude-":
// "anthropic/claude-haiku-4.5" is "haiku-4.5".
func shortModel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return strings.TrimPrefix(model, "claude-")
}

// formatExperiment is the record `experiments show` prints. names maps a
// digest to what it is printed as; a reference names has no entry for is
// printed by shortRef.
func formatExperiment(e *evals.Experiment, parked []*evals.Cell, comparisons []*evals.Comparison, names map[string]string, versions map[string]agentVersion, link string) string {
	named := func(ref string) string { return cmp.Or(names[ref], shortRef(ref)) }
	var b strings.Builder
	reference := ""
	switch {
	case e.Reference.Baseline != "":
		reference = "baseline " + e.Reference.Baseline
	case e.Reference.Candidate != "":
		reference = named(e.Reference.Candidate)
	}
	candidates := make([]string, 0, len(e.Candidates))
	for _, cand := range e.Candidates {
		candidates = append(candidates, named(cand))
	}
	createdBy := ""
	if e.CreatedBy != e.Owner {
		createdBy = e.CreatedBy
	}
	for _, f := range [][2]string{
		{"id", e.ID},
		{"name", experimentName(e)},
		{"suite", named(e.Suite)},
		{"candidates", strings.Join(candidates, ", ")},
		{"reference", reference},
		{"state", string(e.State)},
		{"mode", string(e.Mode)},
		{"trials", fmt.Sprintf("%d per cell", e.Trials)},
		{"parallel", fmt.Sprintf("%d trials at once", e.Parallelism)},
		{"budget", formatBudget(e)},
		{"owner", e.Owner},
		{"created by", createdBy},
		{"started", formatTime(e.CreatedAt)},
	} {
		b.WriteString(formatWrappedField(f[0], stripANSI(f[1])))
	}
	for _, k := range slices.Sorted(maps.Keys(e.Metadata)) {
		if k == experimentNameKey {
			continue
		}
		b.WriteString(formatWrappedField("metadata", stripANSI(k+"="+e.Metadata[k])))
	}

	b.WriteString("\nprogress\n")
	for _, f := range [][2]string{
		{"cells", formatCounts(e.Progress.Cells)},
		{"runs", strings.Replace(runsDone(e), "/", " of ", 1)},
		{"trials", formatCounts(e.Progress.Trials)},
		{"outcomes", formatCounts(e.Progress.Outcomes)},
	} {
		b.WriteString(indent(formatWrappedField(f[0], defaultStr(stripANSI(f[1]), "none"))))
	}
	if len(parked) > 0 {
		b.WriteString("\nparked cells\n")
		var t strings.Builder
		w := tabwriter.NewWriter(&t, 0, 4, 2, ' ', 0)
		fprintln(w, "  CELL\tTASK\tCANDIDATE\tSTATE\tREASON")
		for _, cell := range parked {
			fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", cell.ID, named(cell.Task), named(cell.Candidate), string(cell.State), dash(cell.ParkedReason))
		}
		if err := w.Flush(); err != nil {
			fprintln(&t, err)
		}
		b.WriteString(stripANSI(t.String()))
	}

	b.WriteString("\ncomparisons\n")
	if len(comparisons) == 0 {
		b.WriteString("  none yet\n")
	} else {
		var t strings.Builder
		w := tabwriter.NewWriter(&t, 0, 4, 2, ' ', 0)
		fprintln(w, "  ID\tCOMPARES\tVARIES\tCONFOUNDS\tSTATE\tFIGURES")
		for _, c := range comparisons {
			figures := "provisional"
			if c.Final {
				figures = "final"
			}
			varies, confounds := strings.Join(c.Variable, ", "), c.Confounds
			if v, cf, ok := versionPair(c, versions); ok {
				varies, confounds = v, cf
			}
			fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", c.ID, comparisonSides(c, named), dash(varies),
				dash(strings.Join(confounds, ", ")), string(c.State), figures)
		}
		if err := w.Flush(); err != nil {
			fprintln(&t, err)
		}
		b.WriteString(stripANSI(t.String()))
	}
	b.WriteString("\n" + formatWrappedField("console", link))
	return b.String()
}

// comparisonSides says what a comparison compares: a candidate against the
// reference candidate or a baseline, or one candidate's task against
// another of its tasks. named gives a digest what it is printed as.
func comparisonSides(c *evals.Comparison, named func(string) string) string {
	switch {
	case c.Task != "" && c.ReferenceTask != "":
		return named(c.Candidate) + ": " + named(c.Task) + " vs " + named(c.ReferenceTask)
	case c.Baseline != "":
		return named(c.Candidate) + " vs baseline " + c.Baseline
	case c.ReferenceCandidate != "":
		return named(c.Candidate) + " vs " + named(c.ReferenceCandidate)
	}
	return named(c.Candidate)
}

// formatBudget is an experiment's spending cap, what it spent and what is
// left, as the answer carries them: the figures are absent when no key
// source answered them, and a Budget in another currency names it instead.
func formatBudget(e *evals.Experiment) string {
	b := e.Budget
	var parts []string
	if b.CapMicroUSD != nil {
		parts = append(parts, formatUSD(*b.CapMicroUSD)+" cap")
	}
	if b.SpentMicroUSD != nil {
		parts = append(parts, formatUSD(*b.SpentMicroUSD)+" spent")
	}
	if b.RemainingMicroUSD != nil {
		parts = append(parts, formatUSD(*b.RemainingMicroUSD)+" left")
	}
	if len(parts) == 0 && b.Currency != "" {
		parts = append(parts, "figures in "+b.Currency)
	}
	figures := strings.Join(parts, ", ")
	switch {
	case b.Name != "" && figures != "":
		return figures + " (" + b.Name + ")"
	case b.Name != "":
		return b.Name
	}
	return figures
}

// formatUSD is an amount of micro-dollars as Examen's reports format a
// cost: two decimals from one dollar up, four significant digits below.
func formatUSD(micro int64) string {
	v := float64(micro) / 1e6
	if v > 0 && v < 1 {
		decimals := 3 - int(math.Floor(math.Log10(v)))
		// An amount that rounds up to a dollar is printed as one.
		if s := strconv.FormatFloat(v, 'f', decimals, 64); !strings.HasPrefix(s, "1") {
			return "$" + s
		}
	}
	return fmt.Sprintf("$%.2f", v)
}

// formatCounts is counts by name, "complete 8, running 2", in name order;
// a zero count is left out.
func formatCounts[K ~string](counts map[K]int) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		if counts[k] != 0 {
			parts = append(parts, fmt.Sprintf("%s %d", string(k), counts[k]))
		}
	}
	return strings.Join(parts, ", ")
}

// ---- following an experiment ----

func newEvalsExperimentsWatchCmd(apiURL, authURL *string) *cobra.Command {
	var timeout time.Duration
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "watch <id>",
		Short: "Follow an experiment to its end.",
		Long: `Follow an experiment, started anywhere, the console included, until it is
complete or canceled. Each change prints one line with its time: the
experiment's state with its cells and trials, each cell that changes
state, with the reason of one that parks, and each comparison whose
figures become final. Trials are not printed one by one.

The command exits 0 when the experiment completes with every cell
complete, 1 when it was canceled or completed with a parked cell, 2 when
the experiment cannot be read, and 3 when the service stops answering or
--timeout passes. A dropped connection, and the stream's close when its
token expires, connect again from the last event read, so no event is
shown twice or skipped.

--json prints every frame of the stream as one JSON line: its id, its
event and its data as the API sent them.`,
		Example: `  latere evals experiments watch exp_01jb2c3d4e5f6g7h8j9k0m1n2p
  latere evals experiments watch exp_01jb2c3d4e5f6g7h8j9k0m1n2p --timeout 6h --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newEvalsClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			w := &evalsWatch{c: c, id: args[0], out: cmd.OutOrStdout(), jsonOut: jsonF, now: time.Now}
			err = w.follow(ctx)
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil && cmd.Context().Err() == nil {
				return &exitError{code: exitNoAnswer, err: fmt.Errorf("%s did not end within %s", args[0], timeout)}
			}
			return err
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "stop waiting after this long, such as 30m or 6h (default: no limit)")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print every frame as one JSON line")
	return cmd
}

// evalsWatch follows one experiment's progress stream.
type evalsWatch struct {
	c       *evalsClient
	id      string
	out     io.Writer
	jsonOut bool
	now     func() time.Time

	// lastSeq is the place of the last event read, which a connection
	// resumes after; snapshot reports the snapshot was printed.
	lastSeq  int64
	snapshot bool
	// mode and trials are the experiment's, read from its snapshot.
	mode   evals.Mode
	trials int
	// cells holds each cell's last printed state, so a repeat of a state
	// is not printed again; last is the line printed last.
	cells map[string]evals.CellState
	last  string
}

// follow reads the stream until its end event, connecting again when it
// closes. The SDK's stream connects again after a drop by itself, for as
// long as the API sends anything on it, but gives up after a few
// connections that send nothing at all; so a run of those that lasted is
// followed by another, resuming after the last event read.
func (w *evalsWatch) follow(ctx context.Context) error {
	w.cells = map[string]evals.CellState{}
	for {
		started, read := w.now(), 0
		var streamErr error
		for ev, err := range w.c.ev.StreamEvents(ctx, w.id, &evals.StreamEventsParams{AfterSeq: w.lastSeq}) {
			if err != nil {
				streamErr = err
				break
			}
			read++
			if ev.Seq > 0 {
				w.lastSeq = ev.Seq
			}
			if ev.Type == evals.EventEnd {
				return w.finish(ctx, ev)
			}
			if err := w.print(ev); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if streamErr == nil {
			return &exitError{code: exitNoAnswer, err: fmt.Errorf("the progress stream of %s ended without its end frame", w.id)}
		}
		if e, ok := errors.AsType[*evals.StreamError](streamErr); ok {
			return w.refused(e)
		}
		dropped := errors.Is(streamErr, io.ErrUnexpectedEOF) && (read > 0 || w.now().Sub(started) >= evalsStreamFloor)
		if dropped {
			continue
		}
		if errors.Is(streamErr, io.ErrUnexpectedEOF) {
			return &exitError{code: exitNoAnswer, err: fmt.Errorf("the progress stream of %s could not be kept open: %w", w.id, streamErr)}
		}
		if err := w.c.refused(streamErr); errors.As(err, new(*exitError)) {
			return err
		}
		// An event the stream could not read, such as one whose data is
		// not JSON, may read when the stream is opened again.
		return &exitError{code: exitNoAnswer, err: fmt.Errorf("read the progress stream of %s: %w", w.id, streamErr)}
	}
}

// refused is the error event an open stream ends with: the API's refusal,
// exit code 3 when it says the same stream may succeed later and 2
// otherwise. With --json it is printed first, as the API sent it.
func (w *evalsWatch) refused(e *evals.StreamError) error {
	if w.jsonOut {
		data, err := json.Marshal(map[string]any{"error": map[string]any{"code": e.Code, "message": e.Message, "details": e.Details}})
		if err != nil {
			return fmt.Errorf("render the error frame: %w", err)
		}
		if err := w.printFrame(&evals.StreamEvent{Type: "error", Data: data}); err != nil {
			return err
		}
	}
	code := exitRefused
	if e.Retryable() {
		code = exitNoAnswer
	}
	return &exitError{code: code, err: apiRefusal("Evals", 0, e.Code, e.Message, e.Details)}
}

// print prints one event: as it was sent with --json, else one line for
// the changes worth a line.
func (w *evalsWatch) print(ev *evals.StreamEvent) error {
	if w.jsonOut {
		return w.printFrame(ev)
	}
	line, err := w.line(ev)
	if err != nil {
		return &exitError{code: exitNoAnswer, err: fmt.Errorf("read the %s event of %s: %w", ev.Type, w.id, err)}
	}
	w.printLine(line)
	return nil
}

// printLine prints line with the time, unless it is empty or repeats the
// line printed last.
func (w *evalsWatch) printLine(line string) {
	if line == "" || line == w.last {
		return
	}
	w.last = line
	fprintf(w.out, "%s  %s\n", w.now().Format(time.TimeOnly), stripANSI(line))
}

// printFrame writes an event as one JSON line: its id, which is its place
// in the experiment's log, its event and its data, as the stream sent them.
func (w *evalsWatch) printFrame(ev *evals.StreamEvent) error {
	id := ""
	if ev.Seq > 0 {
		id = strconv.FormatInt(ev.Seq, 10)
	}
	b, err := json.Marshal(struct {
		ID    string          `json:"id,omitempty"`
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data,omitempty"`
	}{id, string(ev.Type), ev.Data})
	if err != nil {
		return fmt.Errorf("render the %s frame: %w", ev.Type, err)
	}
	fprintln(w.out, string(b))
	return nil
}

// line is the line an event prints, or "" for one that prints none: a
// trial, a grading, a provisional comparison, and the events that repeat
// an experiment's or a cell's change.
func (w *evalsWatch) line(ev *evals.StreamEvent) (string, error) {
	switch ev.Type {
	case evals.EventSnapshot:
		if w.snapshot {
			return "", nil
		}
		w.snapshot = true
		var e evals.Experiment
		if err := ev.Decode(&e); err != nil {
			return "", err
		}
		w.mode, w.trials = e.Mode, e.Trials
		return experimentLine(&e), nil
	case evals.EventExperiment:
		var e evals.ExperimentChange
		if err := ev.Decode(&e); err != nil {
			return "", err
		}
		// A change carries the progress alone; the mode and the trials per
		// cell that declare the runs are the snapshot's.
		return experimentLine(&evals.Experiment{ID: cmp.Or(e.ID, w.id), State: e.State, Progress: e.Progress, Mode: w.mode, Trials: w.trials}), nil
	case evals.EventCell:
		var cell evals.CellChange
		if err := ev.Decode(&cell); err != nil {
			return "", err
		}
		if w.cells[cell.ID] == cell.State {
			return "", nil
		}
		w.cells[cell.ID] = cell.State
		line := fmt.Sprintf("cell %s %s × %s: %s", cell.ID, shortRef(cell.Task), shortRef(cell.Candidate), cell.State)
		if cell.ParkedReason != "" {
			line += " (" + cell.ParkedReason + ")"
		}
		return line, nil
	case evals.EventComparison:
		// Read as a whole comparison, so a comparison across tasks names
		// its tasks.
		var c evals.Comparison
		if err := ev.Decode(&c); err != nil {
			return "", err
		}
		if !c.Final {
			return "", nil
		}
		return fmt.Sprintf("comparison %s final: %s", c.ID, comparisonSides(&c, shortRef)), nil
	case "bar.passed", "bar.failed":
		return string(ev.Type) + " " + oneLine(string(ev.Data)), nil
	}
	return "", nil
}

// experimentLine is an experiment's state and its progress on one line.
func experimentLine(e *evals.Experiment) string {
	line := fmt.Sprintf("experiment %s %s", e.ID, e.State)
	if cells := formatCounts(e.Progress.Cells); cells != "" {
		line += " · cells " + cells
	}
	if runs := runsDone(e); runs != "-" {
		line += " · runs done " + strings.Replace(runs, "/", " of ", 1)
	}
	return line
}

// finish handles the end event: the experiment's last state, which reads
// as success only when every cell completed.
func (w *evalsWatch) finish(ctx context.Context, ev *evals.StreamEvent) error {
	if w.jsonOut {
		if err := w.printFrame(ev); err != nil {
			return err
		}
	}
	var end evals.StreamEnd
	if err := ev.Decode(&end); err != nil {
		return &exitError{code: exitNoAnswer, err: fmt.Errorf("read the end of %s's progress stream: %w", w.id, err)}
	}
	e, err := w.c.ev.GetExperiment(ctx, w.id)
	if err != nil {
		return w.c.refused(err)
	}
	parked := 0
	for _, s := range evalsParkedStates {
		parked += e.Progress.Cells[s]
	}
	if !w.jsonOut {
		w.printLine(experimentLine(&evals.Experiment{ID: w.id, State: end.State, Progress: e.Progress, Mode: e.Mode, Trials: e.Trials}))
		fprint(w.out, formatWrappedField("console", w.c.experimentLink(w.id)))
	}
	switch {
	case end.State == evals.ExperimentCanceled:
		return evalsFailed("%s was canceled; every trial it finished is kept", w.id)
	case end.State != evals.ExperimentComplete:
		return evalsFailed("%s ended %s", w.id, end.State)
	case parked > 0:
		return evalsFailed("%s completed with %d parked cells: %s", w.id, parked, formatCounts(e.Progress.Cells))
	}
	return nil
}

// ---- small helpers ----

// shortRef is a reference as a list shows it: a name@digest by its name, a
// bare digest by its first twelve hexadecimal digits, anything else as it
// is.
func shortRef(ref string) string {
	if name, _, ok := strings.Cut(ref, "@"); ok && name != "" {
		return name
	}
	return shortDigest(ref)
}

// shortDigest is a digest by its algorithm and first twelve hexadecimal
// digits, which tell versions apart in a list.
func shortDigest(digest string) string {
	const shown = 12
	if alg, hex, ok := strings.Cut(digest, ":"); ok && len(hex) > shown {
		return alg + ":" + hex[:shown]
	}
	return digest
}

// formatTime is an absolute time in UTC, as a record prints one.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// dash is s, or "-" for an empty cell of a table.
func dash(s string) string {
	return defaultStr(s, "-")
}

// indent indents every line of s by two spaces.
func indent(s string) string {
	if s == "" {
		return ""
	}
	return "  " + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n  ") + "\n"
}
