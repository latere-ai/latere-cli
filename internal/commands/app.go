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

	"latere.ai/x/sdk-go/apps"
	"latere.ai/x/sdk-go/latere"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The app commands reach the Apps API under the platform origin through the
// SDK's apps package, which writes the API's paths under the origin's
// /v1/apps. The base URL a person gives includes that path, as the API's
// address at the origin, and the commands cut it off to name the origin.

// defaultAppURL is the Apps API at the platform origin, its base path
// included.
const defaultAppURL = "https://api.latere.ai/v1/apps"

// appAudience is the aud claim of the bearer every app command presents: the
// Apps API's own audience, which auth mints actor tokens for on the
// latere-cli client. It is the production audience whatever --api-url names:
// the URL selects the deployment, the audience is what the issuer stamps.
const appAudience = "insula"

// appURLUsage is the --api-url help every app command shares.
const appURLUsage = "Apps API base URL, including its /v1/apps path (default " + defaultAppURL + ", or $LATERE_APPS_URL)"

// appRemote is the git remote `app create` adds and every command that
// takes [slug] reads the slug from.
const appRemote = "latere"

// appSlugMax is the most characters an app's slug has.
const appSlugMax = 40

// appSlugHold is how long the platform keeps a slug for the account that gave
// it up, by renaming its app or deleting it, as a sentence says it.
const appSlugHold = "seven days"

// appTimeout bounds one JSON request. A create holds its answer until the
// app's repository exists, which is one round trip to the git host.
const appTimeout = 60 * time.Second

// appListPage is the page size `app list` asks for: the API's largest.
const appListPage = 200

// appReleaseReads bounds the release lists `app list` reads at once.
const appReleaseReads = 8

func newAppCmd() *cobra.Command {
	var apiURL, authURL string
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Create apps, decide who can open them, and follow their builds.",
		Long: `Create apps on the Latere platform, decide who can open them, see their
deploys, and follow their builds.

An app is a git repository and an address. 'latere apps create' makes one in
your current context, your personal account or the organization 'latere org'
selected, and adds its push URL as the git remote latere. Deploying is a git
push, which 'latere login' already lets git sign in for:

  git push latere main      builds a preview of main
  git push latere v1.0.0    releases that commit to the app's address

'latere apps logs -f' follows the build of the commit you pushed and exits
0 when it succeeds and 1 when it fails or is canceled, so
'git push latere main && latere apps logs -f' is the whole loop.

A new app is private: only you, or the members of the organization that owns
it, can open its address, each after signing in with a Latere account. Open
it or share it:

  latere apps visibility unlisted      anyone with the link opens it
  latere apps password                 anyone with the password opens it
  latere apps share ada@example.com    invites one person to a private app
  latere apps access                   shows who can open it

An app becomes public, which lets search engines list it, when it is shown
in the gallery at latere.site. You ask for that from the app's Share in the
chat, and it is reviewed first.

An app's slug is its address, https://<slug>.latere.site, and 'latere apps
rename <new-slug>' changes it. The old address stops working at once.

A command that takes [slug] reads it from the git remote latere of the
repository you run it in when you leave it out.

The commands call https://api.latere.ai/v1/apps with a token minted for your
login. LATERE_APPS_URL or --api-url overrides the address, and
LATERE_APPS_TOKEN presents a bearer as given. The people invited to an app
and its password are kept by the platform, so visibility, password, access,
share and unshare also call https://platform.latere.ai with a token of its
own: LATERE_PLATFORM_URL or --platform-url overrides that address, and
LATERE_PLATFORM_TOKEN presents that bearer as given.`,
		Example: `  latere apps create hello
  git push latere main && latere apps logs -f
  latere apps visibility unlisted
  latere apps password --generate
  latere apps share ada@example.com
  latere apps access
  latere apps rename hello-world
  latere apps list
  latere apps show
  latere apps deploys --json
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
		newAppRenameCmd(&apiURL, &authURL),
		newAppVisibilityCmd(&apiURL, &authURL),
		newAppAccessCmd(&apiURL, &authURL),
		newAppShareCmd(&apiURL, &authURL),
		newAppUnshareCmd(&apiURL, &authURL),
		newAppPasswordCmd(&apiURL, &authURL),
		newAppDeploysCmd(&apiURL, &authURL),
		newAppLogsCmd(&apiURL, &authURL),
		newAppDeleteCmd(&apiURL, &authURL),
	)
	return cmd
}

// ---- the wire ----

// appBasePath is the path the origin serves the Apps API under, which the
// API's base URL ends in.
const appBasePath = "/v1/apps"

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

// answerText is the sentence of a refusal: the API's message, or the text
// of an answer that is not the API's envelope, such as a proxy's page.
// An empty answer has none, where the SDK puts the status's own text.
func answerText(e *latere.Error) string {
	if e.Code == "" && e.Message == http.StatusText(e.StatusCode) {
		return ""
	}
	return e.Message
}

// jsonError is the JSON decoding error err wraps, or nil.
func jsonError(err error) error {
	if e, ok := errors.AsType[*json.SyntaxError](err); ok {
		return e
	}
	if e, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return e
	}
	return nil
}

// appClient is one command's connection to the Apps API: the SDK's apps
// client, and the base URL as the command was given it, which messages
// name.
type appClient struct {
	base string
	apps *apps.Client
}

// newAppClient resolves the API's address, the flag, then $LATERE_APPS_URL,
// then the platform origin, and its bearer: LATERE_APPS_TOKEN as given,
// else an actor token minted for appAudience from the saved login at the
// first request, so the login token never reaches the API. One token
// serves the command while it lives.
func newAppClient(flagURL, authURL string) (*appClient, error) {
	u := flagURL
	if u == "" {
		var err error
		if u, err = capabilityEnv(envAppsURL); err != nil {
			return nil, err
		}
	}
	if u == "" {
		u = defaultAppURL
	}
	base := strings.TrimRight(u, "/")
	origin, ok := strings.CutSuffix(base, appBasePath)
	if !ok {
		return nil, fmt.Errorf("%s is not an Apps API base URL: it ends in %s, as %s does", base, appBasePath, defaultAppURL)
	}
	given, err := capabilityEnv(envAppsToken)
	if err != nil {
		return nil, err
	}
	cfg, err := api.Product{
		BaseURL: origin, AuthURL: authURL, Audience: appAudience,
		Token: strings.TrimSpace(given), Timeout: appTimeout,
	}.Config()
	if err != nil {
		return nil, err
	}
	return &appClient{base: base, apps: apps.New(cfg)}, nil
}

// refused is what a command says of an error a call to the Apps API
// answered: a refusal as an *appError, which the commands explain; a
// bearer that could not be had, an API that could not be reached and an
// answer that could not be read, each in a sentence of its own.
func (c *appClient) refused(err error) error {
	if e, ok := errors.AsType[*api.AuthError](err); ok {
		return fmt.Errorf("cannot authenticate to Apps: %w", e.Err)
	}
	if e, ok := errors.AsType[*latere.Error](err); ok {
		return &appError{Status: e.StatusCode, Code: e.Code, Message: answerText(e), Details: e.Details}
	}
	if e, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("reach the Apps API at %s: %w", c.base, e)
	}
	if e := jsonError(err); e != nil {
		return fmt.Errorf("parse the Apps API's answer: %w", e)
	}
	return err
}

// rawJSON is the API's own bytes of each model, for --json: an empty list
// is [] rather than null.
func rawJSON[T interface{ RawJSON() json.RawMessage }](models []T) []json.RawMessage {
	raws := make([]json.RawMessage, 0, len(models))
	for _, m := range models {
		raws = append(raws, m.RawJSON())
	}
	return raws
}

// liveRelease is the newest release with status released, the one the
// app's address serves, and its index; -1 when there is none.
func liveRelease(releases []*apps.Release) int {
	for i, r := range releases {
		if r.Status == apps.ReleaseReleased {
			return i
		}
	}
	return -1
}

// newestPreview is the index of the newest preview deploy; -1 when there is
// none.
func newestPreview(deploys []*apps.Deploy) int {
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
	// The command as it is written with the slug given: its own arguments
	// with [slug] made required, since the slug is not always the first.
	withSlug := strings.TrimSuffix(cmd.CommandPath(), cmd.Name()) + strings.Replace(cmd.Use, "[slug]", "<slug>", 1)
	missing := fmt.Errorf("missing the [slug] argument: there is no git remote named %s here to read it from; run `%s`", appRemote, withSlug)
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
		return "", false, fmt.Errorf("missing the [slug] argument: the git remote %s (%s) names no app; run `%s`", appRemote, u, withSlug)
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
		slug, remote, visibility string
		noRemote, jsonF          bool
	)
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an app and add its git remote.",
		Long: `Create an app in your current context and print its address, the address of
its newest preview, its push URL, and who can open it.

A new app is private: only you, or the members of the organization that owns
it, and the people you invite can open it, each after signing in with a
Latere account. --visibility unlisted creates it open to anyone who has its
address. 'latere apps visibility' changes that later, 'latere apps share'
invites people to a private app, and 'latere apps password' opens one for
anyone with a password. An app becomes public, which lets search engines
list it, when it is shown in the gallery. You ask for that from the app's
Share in the chat, and it is reviewed first.

The slug is the app's address. --slug names it; without --slug it is derived
from [name], or generated when there is no name. A slug is
` + appSlugGrammar + `, and
` + invalidSlugReasons[slugRuleShort] + `.
A slug you deleted within the last ` + appSlugHold + ` restores that app, and
'latere apps rename' changes an app's slug later.

In a git repository the command adds the push URL as the remote latere, or
the name --remote gives; --no-remote skips that. When the remote already
exists for another app the command refuses and creates nothing.`,
		Example: `  latere apps create
  latere apps create "Hello World"
  latere apps create --slug hello
  latere apps create hello --visibility unlisted
  latere apps create hello --remote deploy
  latere apps create --no-remote --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(remote) == "" {
				return errors.New("--remote needs a name")
			}
			if cmd.Flags().Changed("visibility") {
				if err := checkVisibility(visibility); err != nil {
					return err
				}
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
			// A create that names no visibility leaves it to the platform, which
			// decides what a new app starts as.
			params := &apps.CreateAppParams{Name: firstArg(args), Slug: slug}
			if cmd.Flags().Changed("visibility") {
				params.Visibility = apps.Visibility(visibility)
			}
			c, err := newAppClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			a, err := c.apps.CreateApp(ctx, params)
			if err != nil {
				return explainCreateRefusal(c.refused(err), slug)
			}
			out, notes := cmd.OutOrStdout(), cmd.OutOrStdout()
			if jsonF {
				if err := printRawJSON(out, a.RawJSON()); err != nil {
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
	f.StringVar(&visibility, "visibility", "", "who can open the app, "+visibilityPrivate+" or "+visibilityUnlisted+"; without it the platform decides, and a new app starts private")
	f.StringVar(&remote, "remote", appRemote, "the name of the git remote to add")
	f.BoolVar(&noRemote, "no-remote", false, "do not add a git remote")
	f.BoolVar(&jsonF, "json", false, "print the created app as JSON")
	return cmd
}

func printCreatedApp(out io.Writer, a *apps.App) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString(formatWrappedField("preview", a.PreviewURL))
	b.WriteString(formatWrappedField("push", a.Repository.PushURL))
	b.WriteString(formatWrappedField("visibility", string(a.Visibility)))
	if who := whoOpensSentence(a); who != "" {
		fmt.Fprintf(&b, "\n%s\n", who)
	}
	if a.Visibility == visibilityPrivate {
		fmt.Fprintf(&b, "Open it to anyone who has its address, or invite people by email:\n  latere apps visibility %s %s\n  latere apps share %s <email>...\n", visibilityUnlisted, a.Slug, a.Slug)
	}
	if a.Restored {
		fmt.Fprintf(&b, "\nRestored the deleted app %s. It serves nothing until the next push builds it.\n", a.Slug)
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the created app: %w", err)
	}
	return nil
}

// The rules of a slug, as invalid_slug names the one a slug breaks in its
// reason.
const (
	slugRuleLength       = "length"
	slugRuleCharset      = "charset"
	slugRuleBoundary     = "boundary"
	slugRuleDoubleHyphen = "double_hyphen"
	slugRuleShort        = "short"
)

// invalidSlugReasons says each rule a slug can break, by the reason
// invalid_slug names.
var invalidSlugReasons = map[string]string{
	slugRuleLength:       fmt.Sprintf("a slug is 1 to %d characters", appSlugMax),
	slugRuleCharset:      "a slug takes only lowercase letters, digits and hyphens",
	slugRuleBoundary:     "a slug starts and ends with a letter or a digit",
	slugRuleDoubleHyphen: "a slug has no two hyphens in a row",
	slugRuleShort:        "a slug under three characters needs a plan that allows short slugs",
}

// appSlugGrammar says what a slug is made of, for a command's help.
var appSlugGrammar = fmt.Sprintf("1 to %d lowercase letters, digits and single hyphens", appSlugMax)

// brokenSlugRule is the rule a slug breaks whatever the account's plan, as
// invalid_slug names it, or "" for a slug that breaks none. The platform
// checks the rules in this order and counts a slug in bytes, so the answer
// here is the one it would give. A short slug is a plan's to allow, so only
// the platform refuses one.
func brokenSlugRule(slug string) string {
	switch {
	case slug == "" || len(slug) > appSlugMax:
		return slugRuleLength
	case strings.ContainsFunc(slug, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
	}):
		return slugRuleCharset
	case strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-"):
		return slugRuleBoundary
	case strings.Contains(slug, "--"):
		return slugRuleDoubleHyphen
	}
	return ""
}

// explainSlugRefusal puts what a refusal of a slug names into its sentence:
// the slug, the hold's end, the rule broken. It reports whether the code is
// one a slug earns, at a create and at a rename alike.
func explainSlugRefusal(e *appError, asked string) bool {
	slug := defaultStr(e.detail("slug"), asked)
	switch e.Code {
	case "slug_taken":
		e.explained = fmt.Sprintf("The slug %s is already in use.", slug)
	case "slug_reserved":
		e.explained = fmt.Sprintf("The slug %s is reserved by the platform.", slug)
	case "slug_held":
		e.explained = fmt.Sprintf("The slug %s was released recently and is held for its previous owner.", slug)
		if end, err := time.Parse(time.RFC3339, e.detail("hold_ends_at")); err == nil {
			e.explained = fmt.Sprintf("The slug %s was released recently and is held for its previous owner until %s.", slug, end.UTC().Format(utcMinute))
		}
	case "invalid_slug":
		e.explained = fmt.Sprintf("The slug %s is not a valid address.", slug)
		if rule, ok := invalidSlugReasons[e.detail("reason")]; ok {
			e.explained = fmt.Sprintf("The slug %s is not a valid address: %s.", slug, rule)
		}
	default:
		return false
	}
	return true
}

// explainCreateRefusal puts what each refusal of a create names into its
// sentence: what a slug earns, the stage a repository failed at, the reason
// a create is not allowed.
func explainCreateRefusal(err error, asked string) error {
	e, ok := errors.AsType[*appError](err)
	if !ok {
		return err
	}
	if explainSlugRefusal(e, asked) {
		return e
	}
	switch e.Code {
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
slug, the state, the visibility, the release tag the app's address serves,
and the address.

The visibility says who can open the app: private, only you and the people
invited; unlisted, anyone who has the address; public, anyone, and search
engines may list it. A private app may also open for anyone with its
password, which the list does not read: 'latere apps access' says who can
open one app in full.

'latere org' switches the context. --json prints the apps as the API answers
them.`,
		Example: `  latere apps list
  latere apps list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newAppClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			var list []*apps.App
			for a, err := range c.apps.ListApps(cmd.Context(), &apps.ListAppsParams{Limit: appListPage}) {
				if err != nil {
					return c.refused(err)
				}
				list = append(list, a)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, rawJSON(list))
			}
			production, err := productionTags(cmd.Context(), c, list)
			if err != nil {
				return err
			}
			return printAppList(out, list, production)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// productionTags is the release tag each app's address serves, "-" for one
// that serves none. An app answers no deploy of its own, so each tag is one
// read of the app's releases.
func productionTags(ctx context.Context, c *appClient, list []*apps.App) ([]string, error) {
	tags := make([]string, len(list))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(appReleaseReads)
	for i, a := range list {
		g.Go(func() error {
			releases, err := c.apps.ListReleases(gctx, a.Slug)
			if err != nil {
				return fmt.Errorf("read the releases of %s: %w", a.Slug, c.refused(err))
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

func printAppList(out io.Writer, list []*apps.App, production []string) error {
	var b strings.Builder
	if len(list) == 0 {
		b.WriteString("No apps in your current context. Create one with: latere apps create\n")
	} else {
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "SLUG\tSTATE\tVISIBILITY\tPRODUCTION\tADDRESS")
		for i, a := range list {
			fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.Slug, a.State, defaultStr(string(a.Visibility), "-"), production[i], a.URL)
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
		Short: "Show an app: its addresses, who can open it, production, newest preview and push URL.",
		Long: `Show one app: its visibility with who that lets open it, its address and
the address of its newest preview, the release its address serves with that
release's deploy, its newest preview deploy, and the URLs to push to and
clone from. 'latere apps access' lists the people invited to it, and says
whether a private app also opens with a password.

Without [slug], the slug is read from the git remote latere. --json prints
an object with the app, the release production serves, and the newest
preview deploy, each as the API answers it, or null.`,
		Example: `  latere apps show
  latere apps show hello
  latere apps show hello --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args))
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c, err := newAppClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			a, err := c.apps.GetApp(ctx, slug)
			if err != nil {
				return withSlugSource(c.refused(err), slug, fromRemote)
			}
			releases, err := c.apps.ListReleases(ctx, slug)
			if err != nil {
				return c.refused(err)
			}
			deploys, err := c.apps.ListDeploys(ctx, slug)
			if err != nil {
				return c.refused(err)
			}
			live, preview := liveRelease(releases), newestPreview(deploys)
			out := cmd.OutOrStdout()
			if jsonF {
				shown := struct {
					App     json.RawMessage `json:"app"`
					Release json.RawMessage `json:"release"`
					Preview json.RawMessage `json:"preview"`
				}{App: a.RawJSON(), Release: json.RawMessage("null"), Preview: json.RawMessage("null")}
				if live >= 0 {
					shown.Release = releases[live].RawJSON()
				}
				if preview >= 0 {
					shown.Preview = deploys[preview].RawJSON()
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

func printApp(out io.Writer, a *apps.App, releases []*apps.Release, live int, deploys []*apps.Deploy, preview int) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	if a.Name != a.Slug {
		b.WriteString(formatWrappedField("name", a.Name))
	}
	b.WriteString(formatWrappedField("state", string(a.State)))
	visibility := string(a.Visibility)
	if who := whoOpens(a); who != "" {
		visibility += " (" + who + ")"
	}
	b.WriteString(formatWrappedField("visibility", visibility))
	b.WriteString(formatWrappedField("health", string(a.Health)))
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
					production += " (" + string(d.Status) + ")"
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
deploy of a release. The short id is what 'latere apps logs' takes.

Without [slug], the slug is read from the git remote latere. --json prints
the deploys as the API answers them.`,
		Example: `  latere apps deploys
  latere apps deploys hello
  latere apps deploys hello --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args))
			if err != nil {
				return err
			}
			c, err := newAppClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			deploys, err := c.apps.ListDeploys(cmd.Context(), slug)
			if err != nil {
				return withSlugSource(c.refused(err), slug, fromRemote)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, rawJSON(deploys))
			}
			return printDeploys(out, slug, deploys)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func printDeploys(out io.Writer, slug string, deploys []*apps.Deploy) error {
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
stays held for your account for ` + appSlugHold + ` after the deletion, and creating
an app with that slug within them restores the app and its repository.`,
		Example: `  latere apps delete hello
  latere apps delete hello --yes`,
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
			c, err := newAppClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			if err := c.apps.DeleteApp(cmd.Context(), slug); err != nil {
				return c.refused(err)
			}
			fprintf(cmd.OutOrStdout(), "Deleting %s. Its address no longer serves it.\nThe slug stays held for your account for %s; `latere apps create --slug %s` restores the app until then.\n", slug, appSlugHold, slug)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "delete without asking for the slug")
	return cmd
}
