// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"latere.ai/x/pkg/otel"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The app commands reach the Apps API under the platform origin. The API's
// own routes are written under /v1; at the origin that root is /v1/apps, so
// the API's /v1/apps/{slug} is served at /v1/apps/apps/{slug}. The base URL
// carries the origin's path and every path below is the API's own with /v1
// cut off.

// defaultAppURL is the Apps API at the platform origin, its base path
// included.
const defaultAppURL = "https://api.latere.ai/v1/apps"

// appAudience is the aud claim of the bearer every app command presents: the
// Apps API's own audience, which auth mints actor tokens for on the
// latere-cli client. It is the production audience whatever --api-url names:
// the URL selects the deployment, the audience is what the issuer stamps.
const appAudience = "insula"

// appURLUsage is the --api-url help every app command shares.
const appURLUsage = "Apps API base URL, including its /v1/apps path (default " + defaultAppURL + ", or $LATERE_APP_URL)"

// appRemote is the git remote `app create` adds and every command that
// takes [slug] reads the slug from.
const appRemote = "latere"

// appTimeout bounds one JSON request. A create holds its answer until the
// app's repository exists, which is one round trip to the git host.
const appTimeout = 60 * time.Second

// maxAppResponse bounds a response the CLI reads whole. The deploys of an
// app come in one answer without pages, each deploy about a kilobyte.
const maxAppResponse = 16 << 20

// maxLogLine bounds one line of a build log, as NDJSON or as one SSE data
// field.
const maxLogLine = 1 << 20

// appListPage is the page size `app list` asks for: the API's largest.
const appListPage = 200

// appReleaseReads bounds the release lists `app list` reads at once.
const appReleaseReads = 8

func newAppCmd() *cobra.Command {
	var apiURL, authURL string
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Create apps, see their deploys, and follow their builds.",
		Long: `Create apps on the Latere platform, see their deploys, and follow their
builds.

An app is a git repository and an address. 'latere app create' makes one in
your current context, your personal account or the organization 'latere org'
selected, and adds its push URL as the git remote latere. Deploying is a git
push, which 'latere login' already lets git sign in for:

  git push latere main      builds a preview of main
  git push latere v1.0.0    releases that commit to the app's address

'latere app logs -f' follows the build of the commit you pushed and exits
0 when it succeeds and 1 when it fails or is canceled, so
'git push latere main && latere app logs -f' is the whole loop.

A command that takes [slug] reads it from the git remote latere of the
repository you run it in when you leave it out.

The commands call https://api.latere.ai/v1/apps with a token minted for your
login. LATERE_APP_URL or --api-url overrides the address, and
LATERE_APP_TOKEN presents a bearer as given.`,
		Example: `  latere app create hello
  git push latere main && latere app logs -f
  latere app list
  latere app show
  latere app deploys --json
  git push latere v1.0.0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&apiURL, "api-url", "", appURLUsage)
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the API URL)")
	cmd.AddCommand(
		newAppCreateCmd(&apiURL, &authURL),
		newAppListCmd(&apiURL, &authURL),
		newAppShowCmd(&apiURL, &authURL),
		newAppDeploysCmd(&apiURL, &authURL),
		newAppLogsCmd(&apiURL, &authURL),
		newAppDeleteCmd(&apiURL, &authURL),
	)
	return cmd
}

// ---- the wire ----

// appResource is the App of the API, the fields the commands print. --json
// prints the API's own bytes, so a field read nowhere here still reaches a
// program.
type appResource struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
	State      string `json:"state"`
	Health     string `json:"health"`
	URL        string `json:"url"`
	PreviewURL string `json:"preview_url"`
	Repository struct {
		CloneURL string `json:"clone_url"`
		PushURL  string `json:"push_url"`
	} `json:"repository"`
	Restored bool `json:"restored"`
}

// appDeploy is the Deploy of the API.
type appDeploy struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Preview    bool   `json:"preview"`
	CommitSHA  string `json:"commit_sha"`
	FromDeploy string `json:"from_deploy"`
	Ref        string `json:"ref"`
	URL        string `json:"url"`
	PreviewURL string `json:"preview_url"`
	Components []struct {
		Name string `json:"name"`
	} `json:"components"`
	CreatedAt time.Time `json:"created_at"`
}

// appRelease is the Release of the API.
type appRelease struct {
	Tag    string `json:"tag"`
	Status string `json:"status"`
	Deploy string `json:"deploy"`
}

// appLogLine is one LogLine of a build log.
type appLogLine struct {
	Seq       int64     `json:"seq"`
	TS        time.Time `json:"ts"`
	Src       string    `json:"src"`
	Component string    `json:"component"`
	Line      string    `json:"line"`
}

// appLogEnd is the LogEnd of a build log stream, its last event.
type appLogEnd struct {
	Status string `json:"status"`
	Error  *struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Hint      string `json:"hint"`
		Component string `json:"component"`
	} `json:"error"`
}

// appError is a refusal of the Apps API: the code to branch on, the fixed
// sentence for a person, and the details, which carry the hint and what the
// code names. A command that knows a code better says so in explained,
// which takes the sentence's place.
type appError struct {
	Status    int
	Code      string
	Message   string
	Details   map[string]any
	explained string
	// note is a line the command adds after the hint, such as where an
	// inferred slug came from.
	note string
}

func (e *appError) Error() string {
	sentence := defaultStr(e.explained, e.Message)
	var b strings.Builder
	switch {
	case e.Code != "" && sentence != "":
		b.WriteString(e.Code + ": " + sentence)
	case e.Code != "":
		b.WriteString(e.Code)
	case sentence != "":
		b.WriteString(sentence)
	default:
		fmt.Fprintf(&b, "the Apps API answered HTTP %d", e.Status)
	}
	if hint := e.detail("hint"); hint != "" {
		b.WriteString("\n" + hint)
	}
	if e.Status == http.StatusUnauthorized && e.Code != "audience_mismatch" {
		b.WriteString("\nRun `latere login` and try again.")
	}
	if e.note != "" {
		b.WriteString("\n" + e.note)
	}
	return b.String()
}

// detail is a string the details carry under key, or "".
func (e *appError) detail(key string) string {
	switch v := e.Details[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	default:
		return ""
	}
}

// parseAppError reads a refusal. The API answers its envelope
// {"error": {"code", "message", "details"}}, except for a body it cannot
// read, which it refuses with 400 as plain text; that text, bounded, is the
// sentence.
func parseAppError(status int, body []byte) *appError {
	e := &appError{Status: status}
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Code != "" {
		e.Code, e.Message, e.Details = envelope.Error.Code, envelope.Error.Message, envelope.Error.Details
		return e
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 512 {
		text = text[:512]
	}
	e.Message = text
	return e
}

// appClient is one command's connection to the Apps API: the base URL and
// the bearer, minted on first use and held for the command.
type appClient struct {
	base    string
	authURL string
	bearer  string
}

// newAppClient resolves the API's address: the flag, then $LATERE_APP_URL,
// then the platform origin.
func newAppClient(flagURL, authURL string) *appClient {
	u := flagURL
	if u == "" {
		u = os.Getenv("LATERE_APP_URL")
	}
	if u == "" {
		u = defaultAppURL
	}
	return &appClient{base: strings.TrimRight(u, "/"), authURL: authURL}
}

// token is the bearer: LATERE_APP_TOKEN as given, else an actor token minted
// for appAudience from the saved login, so the login token never reaches
// the API. One token serves the command; it lives five minutes, and a log
// stream needs it only to open.
func (c *appClient) token(ctx context.Context) (string, error) {
	if c.bearer != "" {
		return c.bearer, nil
	}
	if t := strings.TrimSpace(os.Getenv("LATERE_APP_TOKEN")); t != "" {
		c.bearer = t
		return t, nil
	}
	t, _, err := api.ActorToken(ctx, api.ResolveAuthURL(c.base, c.authURL), appAudience)
	if err != nil {
		return "", fmt.Errorf("cannot authenticate to Apps: %w", err)
	}
	c.bearer = t
	return t, nil
}

// request builds one request with the bearer attached.
func (c *appClient) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	bearer, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", "latere-cli")
	return req, nil
}

// call sends one JSON request and answers the body of a 2xx answer. Any
// other answer is an *appError.
func (c *appClient) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := c.request(ctx, method, path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: appTimeout, Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach the Apps API at %s: %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAppResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read the Apps API's answer: %w", err)
	}
	if len(raw) > maxAppResponse {
		return nil, errors.New("the Apps API's answer exceeds 16 MiB")
	}
	if resp.StatusCode/100 != 2 {
		return nil, parseAppError(resp.StatusCode, raw)
	}
	return raw, nil
}

// get sends a GET and decodes the answer into out, keeping the raw bytes
// for --json.
func (c *appClient) get(ctx context.Context, path string, out any) (json.RawMessage, error) {
	raw, err := c.call(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("parse the Apps API's answer: %w", err)
	}
	return raw, nil
}

// stream opens a build log. It has no Timeout: a followed log stays open
// for as long as the build runs, and the command's context bounds it.
func (c *appClient) stream(ctx context.Context, path, accept string) (*http.Response, error) {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	client := &http.Client{Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach the Apps API at %s: %w", c.base, err)
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAppResponse))
		if err != nil {
			return nil, fmt.Errorf("read the Apps API's answer: %w", err)
		}
		return nil, parseAppError(resp.StatusCode, raw)
	}
	return resp, nil
}

// appPath is the path of an app's resource: the app, then each segment of
// rest, every one escaped.
func appPath(slug string, rest ...string) string {
	parts := []string{"/apps", url.PathEscape(slug)}
	for _, r := range rest {
		parts = append(parts, url.PathEscape(r))
	}
	return strings.Join(parts, "/")
}

// getApp reads one app.
func (c *appClient) getApp(ctx context.Context, slug string) (appResource, json.RawMessage, error) {
	var a appResource
	raw, err := c.get(ctx, appPath(slug), &a)
	return a, raw, err
}

// listDeploys reads every deploy of the app, newest first.
func (c *appClient) listDeploys(ctx context.Context, slug string) ([]appDeploy, []json.RawMessage, error) {
	var list struct {
		Deploys []json.RawMessage `json:"deploys"`
	}
	if _, err := c.get(ctx, appPath(slug, "deploys"), &list); err != nil {
		return nil, nil, err
	}
	deploys := make([]appDeploy, len(list.Deploys))
	for i, raw := range list.Deploys {
		if err := json.Unmarshal(raw, &deploys[i]); err != nil {
			return nil, nil, fmt.Errorf("parse the Apps API's answer: %w", err)
		}
	}
	if list.Deploys == nil {
		list.Deploys = []json.RawMessage{}
	}
	return deploys, list.Deploys, nil
}

// listReleases reads every release of the app, newest first.
func (c *appClient) listReleases(ctx context.Context, slug string) ([]appRelease, []json.RawMessage, error) {
	var list struct {
		Releases []json.RawMessage `json:"releases"`
	}
	if _, err := c.get(ctx, appPath(slug, "releases"), &list); err != nil {
		return nil, nil, err
	}
	releases := make([]appRelease, len(list.Releases))
	for i, raw := range list.Releases {
		if err := json.Unmarshal(raw, &releases[i]); err != nil {
			return nil, nil, fmt.Errorf("parse the Apps API's answer: %w", err)
		}
	}
	return releases, list.Releases, nil
}

// liveRelease is the newest release with status released, the one the
// app's address serves, and its index; -1 when there is none.
func liveRelease(releases []appRelease) int {
	for i, r := range releases {
		if r.Status == "released" {
			return i
		}
	}
	return -1
}

// newestPreview is the index of the newest preview deploy; -1 when there is
// none.
func newestPreview(deploys []appDeploy) int {
	for i, d := range deploys {
		if d.Preview {
			return i
		}
	}
	return -1
}

// shortID is the first 8 hexadecimal digits of a deploy id, the prefix its
// own address and the log route take.
func shortID(id string) string {
	h := strings.ReplaceAll(id, "-", "")
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// refName is a full reference name without refs/heads/ or refs/tags/.
func refName(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		if s, ok := strings.CutPrefix(ref, p); ok {
			return s
		}
	}
	return ref
}

// ---- the git remote ----

// gitIn runs git in dir and answers its trimmed standard output.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRepository reports whether dir is in a git repository. A machine
// without git has none.
func gitRepository(ctx context.Context, dir string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	_, err := gitIn(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// remoteURL is the URL configured for the remote name in dir's repository,
// as written in the configuration, and whether the remote exists.
func remoteURL(ctx context.Context, dir, name string) (string, bool, error) {
	u, err := gitIn(ctx, dir, "config", "--get", "remote."+name+".url")
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the git remote %s: %w", name, err)
	}
	return u, true, nil
}

// slugFromRemote is the app slug a push URL names: its last path segment
// without .git, for an https URL and an scp-style SSH address alike.
func slugFromRemote(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return strings.TrimSuffix(u, ".git")
}

// resolveAppSlug is the slug a command acts on: the argument, else the one
// the repository's latere remote names. fromRemote says where it came from,
// so a refusal can say so.
func resolveAppSlug(cmd *cobra.Command, arg string) (slug string, fromRemote bool, err error) {
	if arg != "" {
		return arg, false, nil
	}
	missing := fmt.Errorf("missing the [slug] argument: there is no git remote named %s here to read it from; run `%s <slug>`", appRemote, cmd.CommandPath())
	dir, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("find the current directory: %w", err)
	}
	if !gitRepository(cmd.Context(), dir) {
		return "", false, missing
	}
	u, ok, err := remoteURL(cmd.Context(), dir, appRemote)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, missing
	}
	slug = slugFromRemote(u)
	if slug == "" {
		return "", false, fmt.Errorf("missing the [slug] argument: the git remote %s (%s) names no app; run `%s <slug>`", appRemote, u, cmd.CommandPath())
	}
	return slug, true, nil
}

// withSlugSource adds where an inferred slug came from to an app_not_found
// refusal, the one a stale remote earns.
func withSlugSource(err error, slug string, fromRemote bool) error {
	if e, ok := errors.AsType[*appError](err); ok && fromRemote && e.Code == "app_not_found" {
		e.note = fmt.Sprintf("The slug %s was read from the git remote %s.", slug, appRemote)
	}
	return err
}

// ---- create ----

func newAppCreateCmd(apiURL, authURL *string) *cobra.Command {
	var (
		slug, remote    string
		noRemote, jsonF bool
	)
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an app and add its git remote.",
		Long: `Create an app in your current context and print its address, the address of
its newest preview, and its push URL. The app is public.

The slug is the app's address. --slug names it; without --slug it is derived
from [name], or generated when there is no name. A slug is 1 to 40 lowercase
letters, digits and single hyphens, and a slug under three characters needs a
plan that allows short slugs. A slug you deleted within the last seven days
restores that app.

In a git repository the command adds the push URL as the remote latere, or
the name --remote gives; --no-remote skips that. When the remote already
exists for another app the command refuses and creates nothing.`,
		Example: `  latere app create
  latere app create "Hello World"
  latere app create --slug hello
  latere app create hello --remote deploy
  latere app create --no-remote --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(remote) == "" {
				return errors.New("--remote needs a name")
			}
			ctx := cmd.Context()
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("find the current directory: %w", err)
			}
			inRepo := !noRemote && gitRepository(ctx, dir)
			existing, hasRemote := "", false
			if inRepo {
				existing, hasRemote, err = remoteURL(ctx, dir, remote)
				if err != nil {
					return err
				}
				// A remote that names another app means this repository
				// already deploys somewhere; a create here would leave an
				// app nothing pushes to. A remote naming the slug asked
				// for is the restore of a deleted app.
				if hasRemote && (slug == "" || slugFromRemote(existing) != slug) {
					return fmt.Errorf("this repository already has a git remote named %s, at %s; nothing was created.\nPass --remote <name> to add the new app's push URL under another name, or --no-remote to create the app without one", remote, existing)
				}
			}
			body := map[string]string{"visibility": "public"}
			if len(args) == 1 {
				body["name"] = args[0]
			}
			if slug != "" {
				body["slug"] = slug
			}
			c := newAppClient(*apiURL, *authURL)
			raw, err := c.call(ctx, http.MethodPost, "/apps", body)
			if err != nil {
				return explainCreateRefusal(err, slug)
			}
			var a appResource
			if err := json.Unmarshal(raw, &a); err != nil {
				return fmt.Errorf("parse the Apps API's answer: %w", err)
			}
			out, notes := cmd.OutOrStdout(), cmd.OutOrStdout()
			if jsonF {
				if err := printRawJSON(out, raw); err != nil {
					return err
				}
				notes = cmd.ErrOrStderr()
			} else if err := printCreatedApp(out, a); err != nil {
				return err
			}
			push := a.Repository.PushURL
			next := fmt.Sprintf("git remote add %s %s && git push %s main", remote, push, remote)
			switch {
			case noRemote:
			case !inRepo:
				fprintln(notes, "\nThis directory is not a git repository, so no remote was added.")
			case hasRemote && existing == push:
				fprintf(notes, "\nThe git remote %s already points at the push URL.\n", remote)
				next = "git push " + remote + " main"
			case hasRemote:
				fprintf(notes, "\nThe git remote %s points at %s, not at this app's push URL; it was left as it is.\n", remote, existing)
				next = "git push " + push + " main"
			default:
				if _, err := gitIn(ctx, dir, "remote", "add", remote, push); err != nil {
					return fmt.Errorf("the app %s was created, but adding the git remote %s failed: %w\nAdd it with: git remote add %s %s", a.Slug, remote, err, remote, push)
				}
				fprintf(notes, "\nAdded the git remote %s.\n", remote)
				next = "git push " + remote + " main"
			}
			if !jsonF {
				fprintf(out, "\nNext:\n  %s\n", next)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&slug, "slug", "", "the app's slug, which is its address; derived from [name] or generated without it")
	f.StringVar(&remote, "remote", appRemote, "the name of the git remote to add")
	f.BoolVar(&noRemote, "no-remote", false, "do not add a git remote")
	f.BoolVar(&jsonF, "json", false, "print the created app as JSON")
	return cmd
}

func printCreatedApp(out io.Writer, a appResource) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString(formatWrappedField("preview", a.PreviewURL))
	b.WriteString(formatWrappedField("push", a.Repository.PushURL))
	if a.Restored {
		fmt.Fprintf(&b, "\nRestored the deleted app %s. It serves nothing until the next push builds it.\n", a.Slug)
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the created app: %w", err)
	}
	return nil
}

// invalidSlugReasons says each rule a slug can break, by the reason
// invalid_slug names.
var invalidSlugReasons = map[string]string{
	"length":        "a slug is 1 to 40 characters",
	"charset":       "a slug takes only lowercase letters, digits and hyphens",
	"boundary":      "a slug starts and ends with a letter or a digit",
	"double_hyphen": "a slug has no two hyphens in a row",
	"short":         "a slug under three characters needs a plan that allows short slugs",
}

// explainCreateRefusal puts what each refusal of a create names into its
// sentence: the slug, the hold's end, the rule broken.
func explainCreateRefusal(err error, asked string) error {
	e, ok := errors.AsType[*appError](err)
	if !ok {
		return err
	}
	slug := defaultStr(e.detail("slug"), asked)
	switch e.Code {
	case "slug_taken":
		e.explained = fmt.Sprintf("The slug %s is already in use.", slug)
	case "slug_reserved":
		e.explained = fmt.Sprintf("The slug %s is reserved by the platform.", slug)
	case "slug_held":
		e.explained = fmt.Sprintf("The slug %s was released recently and is held for its previous owner.", slug)
		if end, err := time.Parse(time.RFC3339, e.detail("hold_ends_at")); err == nil {
			e.explained = fmt.Sprintf("The slug %s was released recently and is held for its previous owner until %s.", slug, end.UTC().Format("2006-01-02 15:04 UTC"))
		}
	case "invalid_slug":
		e.explained = fmt.Sprintf("The slug %s is not a valid address.", slug)
		if rule, ok := invalidSlugReasons[e.detail("reason")]; ok {
			e.explained = fmt.Sprintf("The slug %s is not a valid address: %s.", slug, rule)
		}
	case "slug_generation_failed":
		e.explained = fmt.Sprintf("The platform could not find a free address in %s tries. Run the command again, or name a slug with --slug.", defaultStr(e.detail("attempts"), "several"))
	case "repository_create_failed":
		e.explained = "The app's repository could not be created, so no app was created. Run the command again."
		if stage := e.detail("stage"); stage != "" {
			e.explained = fmt.Sprintf("The app's repository could not be created (at %s), so no app was created. Run the command again.", stage)
		}
	case "repository_unavailable":
		e.explained = "The repository of the deleted app this slug restores did not answer, so nothing was restored. Run the command again in a few minutes."
	case "forbidden":
		e.explained = "You may not create an app in your current context."
		if reason := e.detail("reason"); reason != "" {
			e.explained = fmt.Sprintf("You may not create an app in your current context (%s).", reason)
		}
		e.note = "`latere org` shows the context, and switches it."
	case "invalid_request":
		e.explained = fmt.Sprintf("The platform refused the create's %s.", defaultStr(e.detail("field"), "body"))
	case "":
		if e.Status == http.StatusBadRequest {
			e.explained = "The platform could not read the create: " + defaultStr(e.Message, "no reason given") + "."
		}
	}
	return e
}

// ---- list ----

func newAppListCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the apps of your current context.",
		Long: `List the apps of your current context, newest first, one line each: the
slug, the state, the release tag the app's address serves, and the address.

'latere org' switches the context. --json prints the apps as the API answers
them.`,
		Example: `  latere app list
  latere app list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := newAppClient(*apiURL, *authURL)
			apps, raws, err := listApps(cmd.Context(), c)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, raws)
			}
			production, err := productionTags(cmd.Context(), c, apps)
			if err != nil {
				return err
			}
			return printAppList(out, apps, production)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// listApps reads every page of the context's apps.
func listApps(ctx context.Context, c *appClient) ([]appResource, []json.RawMessage, error) {
	apps, raws := []appResource{}, []json.RawMessage{}
	cursor := ""
	for {
		q := url.Values{"limit": {fmt.Sprint(appListPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items      []json.RawMessage `json:"items"`
			HasMore    bool              `json:"has_more"`
			NextCursor string            `json:"next_cursor"`
		}
		if _, err := c.get(ctx, "/apps?"+q.Encode(), &page); err != nil {
			return nil, nil, err
		}
		for _, raw := range page.Items {
			var a appResource
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, nil, fmt.Errorf("parse the Apps API's answer: %w", err)
			}
			apps, raws = append(apps, a), append(raws, raw)
		}
		if !page.HasMore {
			return apps, raws, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return nil, nil, errors.New("the Apps API said more apps follow and named no next page")
		}
		cursor = page.NextCursor
	}
}

// productionTags is the release tag each app's address serves, "-" for one
// that serves none. An app answers no deploy of its own, so each tag is one
// read of the app's releases.
func productionTags(ctx context.Context, c *appClient, apps []appResource) ([]string, error) {
	tags := make([]string, len(apps))
	if len(apps) == 0 {
		return tags, nil
	}
	// Mint before the reads fan out, so they share one token.
	if _, err := c.token(ctx); err != nil {
		return nil, err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(appReleaseReads)
	for i, a := range apps {
		g.Go(func() error {
			releases, _, err := c.listReleases(gctx, a.Slug)
			if err != nil {
				return fmt.Errorf("read the releases of %s: %w", a.Slug, err)
			}
			tags[i] = "-"
			if j := liveRelease(releases); j >= 0 {
				tags[i] = releases[j].Tag
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return tags, nil
}

func printAppList(out io.Writer, apps []appResource, production []string) error {
	var b strings.Builder
	if len(apps) == 0 {
		b.WriteString("No apps in your current context. Create one with: latere app create\n")
	} else {
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "SLUG\tSTATE\tPRODUCTION\tADDRESS")
		for i, a := range apps {
			fprintf(w, "%s\t%s\t%s\t%s\n", a.Slug, a.State, production[i], a.URL)
		}
		_ = w.Flush()
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app list: %w", err)
	}
	return nil
}

// ---- show ----

func newAppShowCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "show [slug]",
		Short: "Show an app: its addresses, production, newest preview and push URL.",
		Long: `Show one app: its address and the address of its newest preview, the
release its address serves with that release's deploy, its newest preview
deploy, and the URLs to push to and clone from.

Without [slug], the slug is read from the git remote latere. --json prints
an object with the app, the release production serves, and the newest
preview deploy, each as the API answers it, or null.`,
		Example: `  latere app show
  latere app show hello
  latere app show hello --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args))
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c := newAppClient(*apiURL, *authURL)
			a, rawApp, err := c.getApp(ctx, slug)
			if err != nil {
				return withSlugSource(err, slug, fromRemote)
			}
			releases, rawReleases, err := c.listReleases(ctx, slug)
			if err != nil {
				return err
			}
			deploys, rawDeploys, err := c.listDeploys(ctx, slug)
			if err != nil {
				return err
			}
			live, preview := liveRelease(releases), newestPreview(deploys)
			out := cmd.OutOrStdout()
			if jsonF {
				shown := struct {
					App     json.RawMessage `json:"app"`
					Release json.RawMessage `json:"release"`
					Preview json.RawMessage `json:"preview"`
				}{App: rawApp, Release: json.RawMessage("null"), Preview: json.RawMessage("null")}
				if live >= 0 {
					shown.Release = rawReleases[live]
				}
				if preview >= 0 {
					shown.Preview = rawDeploys[preview]
				}
				return printJSON(out, shown)
			}
			return printApp(out, a, releases, live, deploys, preview)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func printApp(out io.Writer, a appResource, releases []appRelease, live int, deploys []appDeploy, preview int) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	if a.Name != a.Slug {
		b.WriteString(formatWrappedField("name", a.Name))
	}
	b.WriteString(formatWrappedField("state", a.State))
	b.WriteString(formatWrappedField("visibility", a.Visibility))
	b.WriteString(formatWrappedField("health", a.Health))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString(formatWrappedField("previews", a.PreviewURL))
	production := "none"
	if live >= 0 {
		r := releases[live]
		production = r.Tag
		if r.Deploy != "" {
			production += ", deploy " + shortID(r.Deploy)
			for _, d := range deploys {
				if d.ID == r.Deploy {
					production += " (" + d.Status + ")"
					break
				}
			}
		}
	}
	b.WriteString(formatWrappedField("production", production))
	newest := "none"
	if preview >= 0 {
		d := deploys[preview]
		newest = fmt.Sprintf("%s of %s, %s, %s", shortID(d.ID), refName(d.Ref), d.Status, d.PreviewURL)
	}
	b.WriteString(formatWrappedField("preview", newest))
	b.WriteString(formatWrappedField("push", a.Repository.PushURL))
	b.WriteString(formatWrappedField("clone", a.Repository.CloneURL))
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app: %w", err)
	}
	return nil
}

// ---- deploys ----

func newAppDeploysCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "deploys [slug]",
		Short: "List an app's deploys, newest first.",
		Long: `List the deploys of an app, newest first: the short id, the branch or tag
that was pushed, the status, the deploy's own preview address, and its age.

A pushed branch builds a preview; a pushed tag that starts with v builds the
deploy of a release. The short id is what 'latere app logs' takes.

Without [slug], the slug is read from the git remote latere. --json prints
the deploys as the API answers them.`,
		Example: `  latere app deploys
  latere app deploys hello
  latere app deploys hello --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args))
			if err != nil {
				return err
			}
			deploys, raws, err := newAppClient(*apiURL, *authURL).listDeploys(cmd.Context(), slug)
			if err != nil {
				return withSlugSource(err, slug, fromRemote)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, raws)
			}
			return printDeploys(out, slug, deploys)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func printDeploys(out io.Writer, slug string, deploys []appDeploy) error {
	var b strings.Builder
	if len(deploys) == 0 {
		fmt.Fprintf(&b, "No deploys of %s yet. A push builds one: git push %s main\n", slug, appRemote)
	} else {
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "ID\tREF\tSTATUS\tPREVIEW\tAGE")
		for _, d := range deploys {
			fprintf(w, "%s\t%s\t%s\t%s\t%s\n", shortID(d.ID), refName(d.Ref), d.Status, defaultStr(d.PreviewURL, "-"), humanAge(d.CreatedAt))
		}
		_ = w.Flush()
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the deploys: %w", err)
	}
	return nil
}

// ---- delete ----

func newAppDeleteCmd(apiURL, authURL *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <slug>",
		Short: "Delete an app, its deploys and its repository.",
		Long: `Delete an app. Its address stops serving it at once, and the deletion then
removes the files of its deploys and its repository.

The command asks for the slug typed back unless --yes is given. The slug
stays held for your account for seven days after the deletion, and creating
an app with that slug within them restores the app and its repository.`,
		Example: `  latere app delete hello
  latere app delete hello --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			if !yes {
				fprintf(cmd.ErrOrStderr(), "Delete %s, its deploys and its repository? Type the slug to confirm: ", slug)
				typed, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return fmt.Errorf("read the confirmation: %w", err)
				}
				if strings.TrimSpace(typed) != slug {
					return fmt.Errorf("the typed slug does not match %s; nothing was deleted. Pass --yes to delete without asking", slug)
				}
			}
			if _, err := newAppClient(*apiURL, *authURL).call(cmd.Context(), http.MethodDelete, appPath(slug), nil); err != nil {
				return err
			}
			fprintf(cmd.OutOrStdout(), "Deleting %s. Its address no longer serves it.\nThe slug stays held for your account for seven days; `latere app create --slug %s` restores the app until then.\n", slug, slug)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "delete without asking for the slug")
	return cmd
}
