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
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"latere.ai/x/sdk-go/latere"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The evals commands reach Evals, the platform's capability for experiments
// on agents and on programs built on models, which the Examen core serves
// under the platform origin's /v1/evals (spec 015). They send through the
// Latere Go SDK's request core, Config.Do and Config.Stream, which carries
// the token, the retries and the API's errors; the SDK has no evals package
// yet, so the fields the commands read are declared here, as Examen's
// OpenAPI document names them.

// defaultEvalsURL is the Evals API at the platform origin, its base path
// included.
const defaultEvalsURL = "https://api.latere.ai" + evalsBasePath

// evalsBasePath is the path the origin serves the Evals API under, which
// the API's base URL ends in.
const evalsBasePath = "/v1/evals"

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
const evalsPage = 200

// evalsStreamFloor is how long a run of the progress stream's own
// reconnects must last, when it read no frame, for the watch to connect
// again rather than give up. The API closes a stream at its bearer's
// expiry, and the SDK mints a bearer of at least half a minute, so a quiet
// experiment's stream lasts far longer; one that closes faster is refusing
// to serve. A variable so a test's stub can close at once.
var evalsStreamFloor = 10 * time.Second

// The exit codes of the evals commands (spec 015, after Examen spec 009's
// release check): 0 is done, and a failed command says which of these it
// is, so a CI job can tell a failed result from a refusal from an outage.
const (
	// evalsExitFailure is a result that is a failure, such as a watched
	// experiment that was canceled.
	evalsExitFailure = 1
	// evalsExitRefused is a refusal because of the request: the same
	// command gets the same answer.
	evalsExitRefused = 2
	// evalsExitNoAnswer is no answer yet: the service did not answer or
	// was busy, or a wait ran out. The same command may succeed later.
	evalsExitNoAnswer = 3
)

// evalsExitError is an evals command's error with the exit code
// HandleExitError gives the process.
type evalsExitError struct {
	code int
	err  error
}

func (e *evalsExitError) Error() string { return e.err.Error() }
func (e *evalsExitError) Unwrap() error { return e.err }

// evalsFailed is a result that is a failure, for exit code 1.
func evalsFailed(format string, a ...any) error {
	return &evalsExitError{code: evalsExitFailure, err: fmt.Errorf(format, a...)}
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
experiments and shows their comparisons; 'experiments show' prints the
address of each.

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

// evalsClient is one command's connection to the Evals API: the SDK
// configuration it sends with, the address as the command was given it,
// which messages name, and the console's origin, which links name.
type evalsClient struct {
	cfg     *latere.Config
	base    string
	console string
}

// newEvalsClient resolves the API's address, the flag, then
// $LATERE_EVALS_URL, then the platform origin, and its bearer:
// LATERE_EVALS_TOKEN as given, else actor tokens minted for evalsAudience
// from the saved login and minted again as they lapse, so the login token
// never reaches the API and a long watch keeps working.
func newEvalsClient(flagURL, authURL string) (*evalsClient, error) {
	base := strings.TrimRight(cmp.Or(flagURL, os.Getenv(envEvalsURL), defaultEvalsURL), "/")
	origin, ok := strings.CutSuffix(base, evalsBasePath)
	if !ok {
		return nil, &evalsExitError{code: evalsExitRefused, err: fmt.Errorf("%s is not an Evals API base URL: it ends in %s, as %s does", base, evalsBasePath, defaultEvalsURL)}
	}
	cfg, err := api.Product{
		BaseURL: origin, AuthURL: authURL, Audience: evalsAudience,
		Token: strings.TrimSpace(os.Getenv(envEvalsToken)), Timeout: evalsTimeout,
	}.Config()
	if err != nil {
		return nil, &evalsExitError{code: evalsExitRefused, err: err}
	}
	return &evalsClient{cfg: cfg, base: base, console: resolvePlatformURL("")}, nil
}

// get reads one object at path, below the base path, as the API's bytes.
func (c *evalsClient) get(ctx context.Context, path string) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.cfg.Do(ctx, &latere.Request{Method: http.MethodGet, Path: evalsBasePath + path}, &raw); err != nil {
		return nil, c.refused(err)
	}
	return raw, nil
}

// list reads the list at path with query: its first page, or with all
// every page. more reports a page the read left unread.
func (c *evalsClient) list(ctx context.Context, path string, query url.Values, all bool) (items []json.RawMessage, more bool, err error) {
	fetch := func(ctx context.Context, cursor string) (*latere.Page[json.RawMessage], error) {
		q := url.Values{}
		maps.Copy(q, query)
		q.Set("limit", strconv.Itoa(evalsPage))
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page latere.Page[json.RawMessage]
		if err := c.cfg.Do(ctx, &latere.Request{Method: http.MethodGet, Path: evalsBasePath + path, Query: q}, &page); err != nil {
			return nil, err
		}
		if !all && page.NextCursor != "" {
			more, page.NextCursor = true, ""
		}
		return &page, nil
	}
	items = []json.RawMessage{}
	for item, err := range latere.Items(ctx, fetch) {
		if err != nil {
			return nil, false, c.refused(err)
		}
		items = append(items, item)
	}
	return items, more, nil
}

// refused is what a command says of a call that failed, with its exit
// code: the API's refusal in its code, sentence and the details a person
// can act on; a bearer that could not be had; an API that could not be
// reached; an answer that could not be read.
func (c *evalsClient) refused(err error) error {
	if e, ok := errors.AsType[*latere.TokenError](err); ok {
		code := evalsExitRefused
		if retryable(e.Err) {
			code = evalsExitNoAnswer
		}
		return &evalsExitError{code: code, err: fmt.Errorf("cannot authenticate to Evals: %w", e.Err)}
	}
	if e, ok := errors.AsType[*latere.Error](err); ok {
		code := evalsExitRefused
		if retryableStatus(e.StatusCode) {
			code = evalsExitNoAnswer
		}
		return &evalsExitError{code: code, err: evalsRefusal(e.StatusCode, e.Code, api.RefusalText(e), e.Details)}
	}
	if errors.Is(err, context.DeadlineExceeded) || retryable(err) {
		return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("reach the Evals API at %s: %w", c.base, err)}
	}
	if e := jsonError(err); e != nil {
		return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("read the Evals API's answer: %w", e)}
	}
	return err
}

// retryableStatus reports whether an answer with status may change when the
// same request is sent again: a timeout, a rate limit or a server error.
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// retryable reports whether err is a failure the same request may not meet
// again: a network failure, or an answer retryableStatus names.
func retryable(err error) bool {
	if e, ok := errors.AsType[*latere.Error](err); ok {
		return retryableStatus(e.StatusCode)
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	_, ok := errors.AsType[*url.Error](err)
	return ok
}

// evalsRefusal is the sentence of a refusal: "<code>: <message>", then the
// members of details a person can act on, one per line, and for a 401 the
// command that signs in again.
func evalsRefusal(status int, code, message string, details map[string]any) error {
	var b strings.Builder
	switch {
	case code != "" && message != "":
		b.WriteString(code + ": " + message)
	case code != "":
		b.WriteString(code)
	case message != "":
		b.WriteString(message)
	case status != 0:
		fmt.Fprintf(&b, "the Evals API answered HTTP %d", status)
	default:
		b.WriteString("the Evals API refused without saying why")
	}
	for _, key := range []string{"field", "reason", "constant", "figure"} {
		switch v := details[key].(type) {
		case string:
			if v != "" {
				fmt.Fprintf(&b, "\n%s: %s", key, v)
			}
		case float64:
			fmt.Fprintf(&b, "\n%s: %s", key, strconv.FormatFloat(v, 'g', -1, 64))
		}
	}
	if status == http.StatusUnauthorized {
		b.WriteString("\nRun `latere login` and try again.")
	}
	return errors.New(stripANSI(b.String()))
}

// experimentLink is the console's page of an experiment.
func (c *evalsClient) experimentLink(id string) string {
	return c.console + "/console/evals/experiments/" + url.PathEscape(id)
}

// ---- the definitions ----

// evalsVersion is one version of a definition as the API answers it: its
// record, and beside it the kind's own fields, of which the lists read the
// few their key columns show.
type evalsVersion struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Name      string    `json:"name"`
	Digest    string    `json:"digest"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"created_at"`

	// A metric's type and level, a grader's kind and level, and a
	// candidate's kind.
	Type  string `json:"type"`
	Kind  string `json:"kind"`
	Level string `json:"level"`
	// A suite's tasks and primary metric, and a task's checks.
	Tasks         []string `json:"tasks"`
	PrimaryMetric string   `json:"primary_metric"`
	Checks        []string `json:"checks"`
	// What a candidate runs, by its kind.
	Topos *struct {
		Agent string `json:"agent"`
		Model string `json:"model"`
	} `json:"topos"`
	Program *struct {
		Image string `json:"image"`
	} `json:"program"`
	External *struct {
		Harness string `json:"harness"`
		Version string `json:"version"`
	} `json:"external"`
}

// evalsRecordFields are the members of a version's answer that are its
// record rather than its kind's fields.
var evalsRecordFields = []string{"id", "owner", "name", "digest", "current", "created_at"}

// evalsKind is a kind of definition the API keeps in versions, and how its
// list shows it.
type evalsKind struct {
	// path is the API's collection and the command's word, such as suites.
	path string
	// one names a single object in help and messages.
	one string
	// column and key are the list's key column: its header, and its value
	// for one version.
	column string
	key    func(v evalsVersion) string
}

// evalsKinds are the definitions, in the order an experiment is built from
// them.
var evalsKinds = []evalsKind{
	{path: "metrics", one: "metric", column: "TYPE\tLEVEL", key: func(v evalsVersion) string {
		return dash(v.Type) + "\t" + dash(v.Level)
	}},
	{path: "graders", one: "grader", column: "KIND\tLEVEL", key: func(v evalsVersion) string {
		return dash(v.Kind) + "\t" + dash(v.Level)
	}},
	{path: "tasks", one: "task", column: "CHECKS", key: func(v evalsVersion) string {
		return strconv.Itoa(len(v.Checks))
	}},
	{path: "suites", one: "suite", column: "TASKS\tPRIMARY METRIC", key: func(v evalsVersion) string {
		return strconv.Itoa(len(v.Tasks)) + "\t" + dash(shortRef(v.PrimaryMetric))
	}},
	{path: "candidates", one: "candidate", column: "KIND\tRUNS", key: func(v evalsVersion) string {
		return dash(v.Kind) + "\t" + dash(candidateRuns(v))
	}},
}

// candidateRuns is what a candidate runs, as its kind declares it.
func candidateRuns(v evalsVersion) string {
	switch {
	case v.Topos != nil:
		return strings.TrimSpace(shortRef(v.Topos.Agent) + " " + v.Topos.Model)
	case v.Program != nil:
		return v.Program.Image
	case v.External != nil:
		return strings.TrimSpace(v.External.Harness + " " + v.External.Version)
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
			items, more, err := c.list(cmd.Context(), "/"+kind.path, nil, all)
			if err != nil {
				return err
			}
			if jsonF {
				return printJSON(cmd.OutOrStdout(), items)
			}
			versions, err := decodeAll[evalsVersion](items)
			if err != nil {
				return c.refused(err)
			}
			printVersions(cmd.OutOrStdout(), kind, versions, quiet)
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

func printVersions(out io.Writer, kind evalsKind, versions []evalsVersion, quiet bool) {
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
		fprintf(w, "%s\t%s\t%s\t%s\n", stripANSI(v.Name), shortDigest(v.Digest), stripANSI(kind.key(v)), humanAge(v.CreatedAt))
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
				items, _, err := c.list(cmd.Context(), "/"+kind.path+"/"+url.PathEscape(name)+"/versions", nil, true)
				if err != nil {
					return err
				}
				if jsonF {
					return printJSON(out, items)
				}
				versions, err := decodeAll[evalsVersion](items)
				if err != nil {
					return c.refused(err)
				}
				printVersionHistory(out, versions)
				return nil
			}
			raw, err := c.get(cmd.Context(), "/"+kind.path+"/"+url.PathEscape(args[0]))
			if err != nil {
				return err
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
	var v evalsVersion
	if err := json.Unmarshal(raw, &v); err != nil {
		return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("read the Evals API's answer: %w", err)}
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("read the Evals API's answer: %w", err)}
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

func printVersionHistory(out io.Writer, versions []evalsVersion) {
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

// evalsExperiment is an experiment as the API answers it, the fields the
// commands print.
type evalsExperiment struct {
	ID          string   `json:"id"`
	Owner       string   `json:"owner"`
	Suite       string   `json:"suite"`
	Candidates  []string `json:"candidates"`
	Trials      int      `json:"trials"`
	Parallelism int      `json:"parallelism"`
	Mode        string   `json:"mode"`
	State       string   `json:"state"`
	CreatedBy   string   `json:"created_by"`
	Reference   struct {
		Baseline  string `json:"baseline"`
		Candidate string `json:"candidate"`
	} `json:"reference"`
	Budget struct {
		Name              string `json:"name"`
		CapUSDMicro       *int64 `json:"cap_usd_micro"`
		SpentUSDMicro     *int64 `json:"spent_usd_micro"`
		RemainingUSDMicro *int64 `json:"remaining_usd_micro"`
		Currency          string `json:"currency"`
	} `json:"budget"`
	Progress  evalsProgress     `json:"progress"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// evalsProgress counts an experiment's cells and trials by state, and its
// finished trials by outcome.
type evalsProgress struct {
	Cells    map[string]int `json:"cells"`
	Trials   map[string]int `json:"trials"`
	Outcomes map[string]int `json:"outcomes"`
}

// evalsCell is a cell of an experiment: one task of one candidate.
type evalsCell struct {
	ID           string `json:"id"`
	Task         string `json:"task"`
	Candidate    string `json:"candidate"`
	State        string `json:"state"`
	ParkedReason string `json:"parked_reason"`
}

// evalsComparison is a comparison of an experiment, the fields that say
// what it compares and whether its figures are final.
type evalsComparison struct {
	ID                 string   `json:"id"`
	Candidate          string   `json:"candidate"`
	Baseline           string   `json:"baseline"`
	ReferenceCandidate string   `json:"reference_candidate"`
	Task               string   `json:"task"`
	ReferenceTask      string   `json:"reference_task"`
	Automatic          bool     `json:"automatic"`
	State              string   `json:"state"`
	Final              bool     `json:"final"`
	Variable           []string `json:"variable"`
	Confounds          []string `json:"confounds"`
}

// evalsRetryableStates are the states a parked cell waits in for a retry,
// the ones the API's cell retry resumes. A canceled cell is parked too,
// but its experiment was canceled, so it is not listed one by one.
var evalsRetryableStates = []string{"blocked", "over_budget"}

// evalsParkedStates are the states of a cell that will not finish on its
// own: the retryable ones and canceled.
var evalsParkedStates = append(slices.Clone(evalsRetryableStates), "canceled")

// The states of an experiment the progress stream ends in, and the state
// of a finished trial.
const (
	experimentComplete = "complete"
	experimentCanceled = "canceled"
	trialDone          = "done"
)

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
suite, how many candidates, the state, the mode, the trials done of those
there are, and when it started. The first page holds up to 200; --all reads
every page.

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
			query := url.Values{}
			for k, v := range map[string]string{"suite": suite, "state": state, "mode": mode} {
				if v != "" {
					query.Set(k, v)
				}
			}
			items, more, err := c.list(cmd.Context(), "/experiments", query, all)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, items)
			}
			experiments, err := decodeAll[evalsExperiment](items)
			if err != nil {
				return c.refused(err)
			}
			printExperiments(out, experiments, quiet)
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

func printExperiments(out io.Writer, experiments []evalsExperiment, quiet bool) {
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
	fprintln(w, "ID\tSUITE\tCANDIDATES\tSTATE\tMODE\tTRIALS\tSTARTED")
	for _, e := range experiments {
		fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", stripANSI(e.ID), stripANSI(shortRef(e.Suite)), len(e.Candidates),
			stripANSI(e.State), stripANSI(e.Mode), trialsDone(e.Progress), humanAge(e.CreatedAt))
	}
	if err := w.Flush(); err != nil {
		fprintln(out, err)
	}
}

// trialsDone is "<done>/<all>" of an experiment's trials, or "-" when its
// progress counts none.
func trialsDone(p evalsProgress) string {
	total := 0
	for _, n := range p.Trials {
		total += n
	}
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", p.Trials[trialDone], total)
}

func newEvalsExperimentsShowCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show an experiment, its progress and its comparisons.",
		Long: `Show one experiment: its suite and candidates, its state and mode, the
trials per cell and how many run at once, its spending cap with what it
spent and has left, who started it and when.

Then its progress: its cells and trials by state, and its finished trials
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
			id := args[0]
			raw, err := c.get(cmd.Context(), "/experiments/"+url.PathEscape(id))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, raw)
			}
			var e evalsExperiment
			if err := json.Unmarshal(raw, &e); err != nil {
				return c.refused(err)
			}
			parked, err := c.parkedCells(cmd.Context(), e)
			if err != nil {
				return err
			}
			items, _, err := c.list(cmd.Context(), "/experiments/"+url.PathEscape(e.ID)+"/comparisons", nil, true)
			if err != nil {
				return err
			}
			comparisons, err := decodeAll[evalsComparison](items)
			if err != nil {
				return c.refused(err)
			}
			names, err := c.names(cmd.Context(), e, parked)
			if err != nil {
				return err
			}
			fprint(out, formatExperiment(e, parked, comparisons, names, c.experimentLink(e.ID)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// parkedCells reads the cells of e that wait for a retry, the states its
// progress counts any in alone.
func (c *evalsClient) parkedCells(ctx context.Context, e evalsExperiment) ([]evalsCell, error) {
	var cells []evalsCell
	for _, state := range evalsRetryableStates {
		if e.Progress.Cells[state] == 0 {
			continue
		}
		items, _, err := c.list(ctx, "/experiments/"+url.PathEscape(e.ID)+"/cells", url.Values{"state": {state}}, true)
		if err != nil {
			return nil, err
		}
		some, err := decodeAll[evalsCell](items)
		if err != nil {
			return nil, c.refused(err)
		}
		cells = append(cells, some...)
	}
	return cells, nil
}

// names reads the name of each definition an experiment names by digest
// alone, its suite, its candidates and the tasks of its parked cells, and
// answers each digest's name@digest. A definition the caller may not read,
// such as another owner's suite, reads as missing and keeps its digest.
func (c *evalsClient) names(ctx context.Context, e evalsExperiment, parked []evalsCell) (map[string]string, error) {
	names := map[string]string{}
	add := func(kind, ref string) error {
		if _, done := names[ref]; done || !strings.HasPrefix(ref, "sha256:") {
			return nil
		}
		var v evalsVersion
		err := c.cfg.Do(ctx, &latere.Request{Method: http.MethodGet, Path: evalsBasePath + "/" + kind + "/" + url.PathEscape(ref)}, &v)
		switch {
		case errors.Is(err, latere.ErrNotFound):
			names[ref] = ref
		case err != nil:
			return c.refused(err)
		default:
			names[ref] = v.Name + "@" + ref
		}
		return nil
	}
	refs := [][2]string{{"suites", e.Suite}}
	for _, cand := range append(slices.Clone(e.Candidates), e.Reference.Candidate) {
		refs = append(refs, [2]string{"candidates", cand})
	}
	for _, cell := range parked {
		refs = append(refs, [2]string{"tasks", cell.Task}, [2]string{"candidates", cell.Candidate})
	}
	for _, r := range refs {
		if err := add(r[0], r[1]); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// formatExperiment is the record `experiments show` prints. names maps a
// digest to the name@digest it is printed as.
func formatExperiment(e evalsExperiment, parked []evalsCell, comparisons []evalsComparison, names map[string]string, link string) string {
	named := func(ref string) string { return cmp.Or(names[ref], ref) }
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
		{"suite", named(e.Suite)},
		{"candidates", strings.Join(candidates, ", ")},
		{"reference", reference},
		{"state", e.State},
		{"mode", e.Mode},
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
		b.WriteString(formatWrappedField("metadata", stripANSI(k+"="+e.Metadata[k])))
	}

	b.WriteString("\nprogress\n")
	for _, f := range [][2]string{
		{"cells", formatCounts(e.Progress.Cells)},
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
			fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", cell.ID, shortRef(named(cell.Task)), shortRef(named(cell.Candidate)), cell.State, dash(cell.ParkedReason))
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
			fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", c.ID, comparisonSides(c, named), dash(strings.Join(c.Variable, ", ")),
				dash(strings.Join(c.Confounds, ", ")), c.State, figures)
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
// another of its tasks. named gives a digest its name@digest.
func comparisonSides(c evalsComparison, named func(string) string) string {
	short := func(ref string) string { return shortRef(named(ref)) }
	switch {
	case c.Task != "" && c.ReferenceTask != "":
		return short(c.Candidate) + ": " + short(c.Task) + " vs " + short(c.ReferenceTask)
	case c.Baseline != "":
		return short(c.Candidate) + " vs baseline " + c.Baseline
	case c.ReferenceCandidate != "":
		return short(c.Candidate) + " vs " + short(c.ReferenceCandidate)
	}
	return short(c.Candidate)
}

// formatBudget is an experiment's spending cap, what it spent and what is
// left, as the answer carries them: the figures are absent when no key
// source answered them, and a Budget in another currency names it instead.
func formatBudget(e evalsExperiment) string {
	b := e.Budget
	var parts []string
	if b.CapUSDMicro != nil {
		parts = append(parts, formatUSD(*b.CapUSDMicro)+" cap")
	}
	if b.SpentUSDMicro != nil {
		parts = append(parts, formatUSD(*b.SpentUSDMicro)+" spent")
	}
	if b.RemainingUSDMicro != nil {
		parts = append(parts, formatUSD(*b.RemainingUSDMicro)+" left")
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
func formatCounts(counts map[string]int) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		if counts[k] != 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
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
				return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("%s did not end within %s", args[0], timeout)}
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

	// lastID is the id of the last frame read, which a connection resumes
	// after; snapshot reports the snapshot was printed.
	lastID   string
	snapshot bool
	// cells holds each cell's last printed state, so a repeat of a state
	// is not printed again; last is the line printed last.
	cells map[string]string
	last  string
}

// follow reads the stream until its end frame, connecting again when it
// closes. The SDK's stream connects again after a drop by itself, but
// gives up after a few connections that read no frame, which a quiet
// experiment's stream does at each token expiry; so a run of those that
// lasted is followed by another, resuming after the last frame read.
func (w *evalsWatch) follow(ctx context.Context) error {
	w.cells = map[string]string{}
	for {
		started, read := w.now(), 0
		req := &latere.Request{Method: http.MethodGet, Path: evalsBasePath + "/experiments/" + url.PathEscape(w.id) + "/stream"}
		if w.lastID != "" {
			req.Header = http.Header{"Last-Event-ID": {w.lastID}}
		}
		var end *latere.Frame
		var streamErr error
		for frame, err := range w.c.cfg.Stream(ctx, req, isStreamEnd) {
			if err != nil {
				streamErr = err
				break
			}
			read++
			if frame.ID != "" {
				w.lastID = frame.ID
			}
			if isStreamEnd(frame) {
				end = &frame
				break
			}
			if err := w.print(frame); err != nil {
				return err
			}
		}
		if end != nil {
			return w.finish(ctx, *end)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if streamErr == nil {
			return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("the progress stream of %s ended without its end frame", w.id)}
		}
		dropped := errors.Is(streamErr, io.ErrUnexpectedEOF) && (read > 0 || w.now().Sub(started) >= evalsStreamFloor)
		if !dropped {
			if errors.Is(streamErr, io.ErrUnexpectedEOF) {
				return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("the progress stream of %s could not be kept open: %w", w.id, streamErr)}
			}
			return w.c.refused(streamErr)
		}
	}
}

// isStreamEnd reports the frames after which the server closes the stream:
// end, once the experiment is finished, and error.
func isStreamEnd(f latere.Frame) bool {
	return f.Event == "end" || f.Event == "error"
}

// print prints one frame: as it was sent with --json, else one line for
// the changes worth a line.
func (w *evalsWatch) print(f latere.Frame) error {
	if w.jsonOut {
		return w.printFrame(f)
	}
	line, err := w.line(f)
	if err != nil {
		return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("read the %s event of %s: %w", f.Event, w.id, err)}
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

// printFrame writes f as one JSON line: its id, event and data.
func (w *evalsWatch) printFrame(f latere.Frame) error {
	data := json.RawMessage(f.Data)
	if !json.Valid(data) {
		data = nil
	}
	b, err := json.Marshal(struct {
		ID    string          `json:"id,omitempty"`
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data,omitempty"`
	}{f.ID, f.Event, data})
	if err != nil {
		return fmt.Errorf("render the %s frame: %w", f.Event, err)
	}
	fprintln(w.out, string(b))
	return nil
}

// line is the line a frame prints, or "" for one that prints none: a
// trial, a grading, a provisional comparison, and the events that repeat
// an experiment's or a cell's change.
func (w *evalsWatch) line(f latere.Frame) (string, error) {
	switch f.Event {
	case "snapshot":
		if w.snapshot {
			return "", nil
		}
		w.snapshot = true
		var e evalsExperiment
		if err := json.Unmarshal(f.Data, &e); err != nil {
			return "", err
		}
		return experimentLine(e.ID, e.State, e.Progress), nil
	case "experiment":
		var e evalsExperiment
		if err := json.Unmarshal(f.Data, &e); err != nil {
			return "", err
		}
		return experimentLine(cmp.Or(e.ID, w.id), e.State, e.Progress), nil
	case "cell":
		var cell evalsCell
		if err := json.Unmarshal(f.Data, &cell); err != nil {
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
	case "comparison":
		var c evalsComparison
		if err := json.Unmarshal(f.Data, &c); err != nil {
			return "", err
		}
		if !c.Final {
			return "", nil
		}
		return fmt.Sprintf("comparison %s final: %s", c.ID, comparisonSides(c, func(ref string) string { return ref })), nil
	case "bar.passed", "bar.failed":
		return f.Event + " " + oneLine(string(f.Data)), nil
	}
	return "", nil
}

// experimentLine is an experiment's state and its progress on one line.
func experimentLine(id, state string, p evalsProgress) string {
	line := fmt.Sprintf("experiment %s %s", id, state)
	if cells := formatCounts(p.Cells); cells != "" {
		line += " · cells " + cells
	}
	if trials := trialsDone(p); trials != "-" {
		line += " · trials done " + strings.Replace(trials, "/", " of ", 1)
	}
	return line
}

// finish handles the frame that ended the stream: an error frame is the
// API's refusal; an end frame is the experiment's last state, which reads
// as success only when every cell completed.
func (w *evalsWatch) finish(ctx context.Context, f latere.Frame) error {
	if w.jsonOut {
		if err := w.printFrame(f); err != nil {
			return err
		}
	}
	if f.Event == "error" {
		var envelope struct {
			Error struct {
				Code    string         `json:"code"`
				Message string         `json:"message"`
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(f.Data, &envelope); err != nil {
			return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("the progress stream of %s ended with an error that could not be read: %w", w.id, err)}
		}
		code := evalsExitRefused
		if again, _ := envelope.Error.Details["retryable"].(bool); again {
			code = evalsExitNoAnswer
		}
		e := envelope.Error
		return &evalsExitError{code: code, err: evalsRefusal(0, e.Code, e.Message, e.Details)}
	}
	var end struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(f.Data, &end); err != nil {
		return &evalsExitError{code: evalsExitNoAnswer, err: fmt.Errorf("read the end of %s's progress stream: %w", w.id, err)}
	}
	raw, err := w.c.get(ctx, "/experiments/"+url.PathEscape(w.id))
	if err != nil {
		return err
	}
	var e evalsExperiment
	if err := json.Unmarshal(raw, &e); err != nil {
		return w.c.refused(err)
	}
	parked := 0
	for _, s := range evalsParkedStates {
		parked += e.Progress.Cells[s]
	}
	if !w.jsonOut {
		w.printLine(experimentLine(w.id, end.State, e.Progress))
		fprint(w.out, formatWrappedField("console", w.c.experimentLink(w.id)))
	}
	switch {
	case end.State == experimentCanceled:
		return evalsFailed("%s was canceled; every trial it finished is kept", w.id)
	case end.State != experimentComplete:
		return evalsFailed("%s ended %s", w.id, end.State)
	case parked > 0:
		return evalsFailed("%s completed with %d parked cells: %s", w.id, parked, formatCounts(e.Progress.Cells))
	}
	return nil
}

// ---- small helpers ----

// decodeAll decodes each item of a list into T.
func decodeAll[T any](items []json.RawMessage) ([]T, error) {
	out := make([]T, 0, len(items))
	for _, item := range items {
		var v T
		if err := json.Unmarshal(item, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

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
