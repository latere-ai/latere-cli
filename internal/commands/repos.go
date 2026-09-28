// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/pkg/otel"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The repository commands reach platformd's repository routes on the
// platform's public host. platformd is the one writer of a repository: a
// create records it on the platform and creates it at Latere Code in one
// step, which is why the CLI creates here and not at Latere Code's own API,
// which refuses a person's create.

// defaultPlatformURL is the host platformd's public routes are served at.
const defaultPlatformURL = "https://platform.latere.ai"

// platformAudience is the aud claim platformd verifies on every bearer its
// public routes take. It is the production audience whatever
// --platform-url names: the URL selects the deployment, the audience is
// what the issuer stamps.
const platformAudience = "api.latere.ai"

// platformURLUsage is the --platform-url help every repos command shares.
const platformURLUsage = "platform base URL (default " + defaultPlatformURL + ", or $LATERE_PLATFORM_URL)"

// platformTimeout bounds one request. A create holds its answer until
// Latere Code has created the repository, which is one round trip there.
const platformTimeout = 60 * time.Second

// maxPlatformResponse bounds a response the CLI reads whole. A context's
// list is at most the owner's repository cap plus what is shared with the
// person, each row a few hundred bytes.
const maxPlatformResponse = 4 << 20

func newReposCmd() *cobra.Command {
	var platformURL, authURL string
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "Create, list, and look up your repositories on Latere Code.",
		Long: `Create, list, and look up Git repositories on Latere Code.

Every command acts in your current context: your personal account, or the
organization 'latere org' selected. A repository lives under an owner, your
handle or the organization's short name, and is addressed as <owner>/<name>.
In an organization, its owners and admins create repositories.

Clone and push over git at code.latere.ai; 'latere git-credential setup'
lets git sign in with your login.

The commands call the platform at https://platform.latere.ai with a token
minted for your login. LATERE_PLATFORM_URL or --platform-url overrides the
address, and LATERE_PLATFORM_TOKEN presents a bearer as given.`,
		Example: `  latere repos create alice/notes
  latere repos create alice/site --public
  latere repos list
  latere repos get alice/notes --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the platform URL)")
	cmd.AddCommand(newReposCreateCmd(&platformURL, &authURL))
	cmd.AddCommand(newReposListCmd(&platformURL, &authURL))
	cmd.AddCommand(newReposGetCmd(&platformURL, &authURL))
	return cmd
}

// ---- the wire ----

// createdRepository is the answer of POST /repositories.
type createdRepository struct {
	ID         string `json:"id"`
	OwnerType  string `json:"owner_type"`
	OwnerID    string `json:"owner_id"`
	OwnerLabel string `json:"owner_label"`
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}

// listedRepository is one row of GET /repositories.
type listedRepository struct {
	ID         string    `json:"id"`
	OwnerType  string    `json:"owner_type"`
	OwnerID    string    `json:"owner_id"`
	OwnerLabel string    `json:"owner_label"`
	Slug       string    `json:"slug"`
	Visibility string    `json:"visibility"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
}

// reposContext is the answer of GET /repositories: the context the token is
// active in, the repositories its owner holds, and, in the personal
// context, the repositories others share with the person.
type reposContext struct {
	Context struct {
		OwnerType string `json:"owner_type"`
		OwnerID   string `json:"owner_id"`
		Label     string `json:"label"`
		Name      string `json:"name"`
		Create    struct {
			Allowed   bool   `json:"allowed"`
			Reason    string `json:"reason,omitempty"`
			Remaining int    `json:"remaining"`
		} `json:"create"`
	} `json:"context"`
	Repositories []listedRepository `json:"repositories"`
	Shared       []listedRepository `json:"shared"`
}

// platformError is a refusal from platformd: a code to branch on, the
// sentence written for a person, and the detail written for a developer.
// The repository routes answer {"error": "<code>", "message", "detail"};
// the shared envelope {"error": {"code", "message", "details"}} is read as
// well, so a refusal in either shape keeps its code and sentence.
type platformError struct {
	Status  int
	Code    string
	Message string
	Detail  string
}

func (e *platformError) Error() string {
	head := e.Message
	switch {
	case e.Code != "" && head != "":
		head = e.Code + ": " + head
	case e.Code != "":
		head = e.Code
	case head == "":
		head = fmt.Sprintf("the platform answered HTTP %d", e.Status)
	}
	if e.Detail != "" {
		return head + "\ndetail: " + e.Detail
	}
	return head
}

// parsePlatformError reads a refusal body in either envelope. A body that is
// neither keeps its text, bounded, as the message.
func parsePlatformError(status int, body []byte) *platformError {
	e := &platformError{Status: status}
	var flat struct {
		Error   json.RawMessage `json:"error"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(body, &flat) == nil {
		e.Code, e.Message, e.Detail = flat.Code, flat.Message, flat.Detail
		var code string
		var nested struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		}
		switch {
		case len(flat.Error) == 0:
		case json.Unmarshal(flat.Error, &code) == nil:
			e.Code = code
		case json.Unmarshal(flat.Error, &nested) == nil:
			e.Code = nested.Code
			if nested.Message != "" {
				e.Message = nested.Message
			}
		}
		if e.Code != "" || e.Message != "" {
			return e
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 512 {
		text = text[:512]
	}
	e.Message = text
	return e
}

// resolvePlatformURL returns the platform's address: the flag, then
// $LATERE_PLATFORM_URL, then the public host.
func resolvePlatformURL(flagURL string) string {
	u := flagURL
	if u == "" {
		u = os.Getenv("LATERE_PLATFORM_URL")
	}
	if u == "" {
		u = defaultPlatformURL
	}
	return strings.TrimRight(u, "/")
}

// platformBearer is the bearer a repos command presents: LATERE_PLATFORM_TOKEN
// as given, else an actor token minted for platformAudience from the saved
// login, so the login token never reaches the platform.
func platformBearer(ctx context.Context, base, authURL string) (string, error) {
	if t := strings.TrimSpace(os.Getenv("LATERE_PLATFORM_TOKEN")); t != "" {
		return t, nil
	}
	token, _, err := api.ActorToken(ctx, api.ResolveAuthURL(base, authURL), platformAudience)
	if err != nil {
		return "", fmt.Errorf("cannot authenticate to the platform: %w", err)
	}
	return token, nil
}

// platformCall sends one request to the platform with a fresh bearer and
// decodes a JSON answer into out. A non-2xx answer is a *platformError.
func platformCall(ctx context.Context, platformURL, authURL, method, path string, body, out any) error {
	base := resolvePlatformURL(platformURL)
	bearer, err := platformBearer(ctx, base, authURL)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", "latere-cli")
	client := &http.Client{Timeout: platformTimeout, Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reach the platform at %s: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPlatformResponse+1))
	if err != nil {
		return fmt.Errorf("read the platform's answer: %w", err)
	}
	if len(raw) > maxPlatformResponse {
		return errors.New("the platform's answer exceeds 4 MiB")
	}
	if resp.StatusCode/100 != 2 {
		return parsePlatformError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse the platform's answer: %w", err)
	}
	return nil
}

// wrapReposErr adds what to do next to a refused token, leaving other
// errors untouched: every other refusal's sentence and detail already say
// what the rule is.
func wrapReposErr(err error) error {
	if pe, ok := errors.AsType[*platformError](err); ok && pe.Status == http.StatusUnauthorized {
		return fmt.Errorf("%w\nRun `latere login` and try again", err)
	}
	return err
}

// splitRepositoryName reads <owner>/<name>. The server holds the naming
// rules; this only refuses what cannot be an address at all.
func splitRepositoryName(arg string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(arg), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("%q is not <owner>/<name>, for example alice/notes", arg)
	}
	return owner, name, nil
}

// newRepositoryID is a random lower-case UUID, version 4. The caller names
// the repository's id, so the id is chosen once per create and a retried
// request is the same repository.
func newRepositoryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("choose a repository id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// codeHost is the git host clone URLs name: CODE_HOST, the same override the
// git credential helper reads, or the public host.
func codeHost() string {
	if v := strings.TrimSpace(os.Getenv("CODE_HOST")); v != "" {
		return v
	}
	return defaultCodeHost
}

// cloneURLs are the https and ssh remotes of owner/slug.
func cloneURLs(owner, slug string) (httpsURL, sshURL string) {
	host := codeHost()
	return "https://" + host + "/" + owner + "/" + slug + ".git", "git@" + host + ":" + owner + "/" + slug + ".git"
}

// ---- create ----

func newReposCreateCmd(platformURL, authURL *string) *cobra.Command {
	var public, jsonF bool
	cmd := &cobra.Command{
		Use:   "create <owner>/<name>",
		Short: "Create a repository.",
		Long: `Create a repository under an owner: your handle, or the short name of the
organization you are in as an owner or admin. It is private unless --public
is given, and you can change that later in the console.

The owner must be one you may create under in your current context:
'latere org --personal' for your handle, 'latere org <org-id>' for an
organization. A name is letters, digits, and . _ - up to 64 of them.

The repository is created empty. Push a first commit to it over git.`,
		Example: `  latere repos create alice/notes
  latere repos create acme/site --public
  latere repos create alice/notes --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepositoryName(args[0])
			if err != nil {
				return err
			}
			id, err := newRepositoryID()
			if err != nil {
				return err
			}
			visibility := "private"
			if public {
				visibility = "public"
			}
			body := map[string]string{"id": id, "owner_label": owner, "slug": name, "visibility": visibility}
			var created createdRepository
			if err := platformCall(cmd.Context(), *platformURL, *authURL, http.MethodPost, "/repositories", body, &created); err != nil {
				return wrapReposErr(err)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, created)
			}
			return printCreatedRepository(out, created)
		},
	}
	cmd.Flags().BoolVar(&public, "public", false, "make the repository readable and clonable by anyone, with no credential")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func printCreatedRepository(out io.Writer, r createdRepository) error {
	httpsURL, sshURL := cloneURLs(r.OwnerLabel, r.Slug)
	var b strings.Builder
	b.WriteString(formatWrappedField("repository", r.OwnerLabel+"/"+r.Slug))
	b.WriteString(formatWrappedField("id", r.ID))
	b.WriteString(formatWrappedField("visibility", r.Visibility))
	b.WriteString(formatWrappedField("https", httpsURL))
	b.WriteString(formatWrappedField("ssh", sshURL))
	b.WriteString("\nPush a first commit:\n")
	fmt.Fprintf(&b, "  git remote add origin %s\n  git push -u origin main\n", httpsURL)
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the created repository: %w", err)
	}
	return nil
}

// ---- list ----

func newReposListCmd(platformURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the repositories of your current context.",
		Long: `List the repositories your current context's owner holds, and in your
personal context the ones others share with you, with your role on each.

'latere org' switches the context.`,
		Example: `  latere repos list
  latere repos list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := listRepositories(cmd.Context(), *platformURL, *authURL)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, c)
			}
			return printRepositoryList(out, c)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func listRepositories(ctx context.Context, platformURL, authURL string) (reposContext, error) {
	var c reposContext
	if err := platformCall(ctx, platformURL, authURL, http.MethodGet, "/repositories", nil, &c); err != nil {
		return reposContext{}, wrapReposErr(err)
	}
	if c.Repositories == nil {
		c.Repositories = []listedRepository{}
	}
	if c.Shared == nil {
		c.Shared = []listedRepository{}
	}
	return c, nil
}

func printRepositoryList(out io.Writer, c reposContext) error {
	var b strings.Builder
	owner := defaultStr(c.Context.Name, c.Context.Label)
	if owner == "" {
		owner = "your personal account"
	}
	if len(c.Repositories) == 0 && len(c.Shared) == 0 {
		fmt.Fprintf(&b, "No repositories in %s.\n", owner)
	} else {
		table := func(rows []listedRepository) {
			w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
			fprintln(w, "REPOSITORY\tVISIBILITY\tROLE\tCREATED")
			for _, r := range rows {
				fprintf(w, "%s/%s\t%s\t%s\t%s\n", r.OwnerLabel, r.Slug, r.Visibility, r.Role, humanAge(r.CreatedAt))
			}
			_ = w.Flush()
		}
		if len(c.Repositories) > 0 {
			table(c.Repositories)
		} else {
			fmt.Fprintf(&b, "No repositories in %s.\n", owner)
		}
		if len(c.Shared) > 0 {
			b.WriteString("\nShared with you:\n")
			table(c.Shared)
		}
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the repository list: %w", err)
	}
	return nil
}

// ---- get ----

func newReposGetCmd(platformURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "get <owner>/<name>",
		Short: "Show one repository of your current context.",
		Long: `Show one repository by its owner and name: its id, visibility, your role,
and its clone URLs.

It finds repositories the way 'latere repos list' lists them: those your
current context's owner holds, and in your personal context those shared
with you. 'latere org' switches the context.`,
		Example: `  latere repos get alice/notes
  latere repos get acme/site --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepositoryName(args[0])
			if err != nil {
				return err
			}
			c, err := listRepositories(cmd.Context(), *platformURL, *authURL)
			if err != nil {
				return err
			}
			r, ok := findRepository(c, owner, name)
			if !ok {
				return fmt.Errorf("no repository %s/%s in your current context; `latere repos list` shows the repositories you can reach here, and `latere org` switches the context", owner, name)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, r)
			}
			return printRepository(out, r)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

// findRepository is the row owner/name names, compared without case as the
// platform compares labels.
func findRepository(c reposContext, owner, name string) (listedRepository, bool) {
	for _, rows := range [][]listedRepository{c.Repositories, c.Shared} {
		for _, r := range rows {
			if strings.EqualFold(r.OwnerLabel, owner) && strings.EqualFold(r.Slug, name) {
				return r, true
			}
		}
	}
	return listedRepository{}, false
}

func printRepository(out io.Writer, r listedRepository) error {
	httpsURL, sshURL := cloneURLs(r.OwnerLabel, r.Slug)
	var b strings.Builder
	b.WriteString(formatWrappedField("repository", r.OwnerLabel+"/"+r.Slug))
	b.WriteString(formatWrappedField("id", r.ID))
	b.WriteString(formatWrappedField("visibility", r.Visibility))
	b.WriteString(formatWrappedField("role", r.Role))
	if !r.CreatedAt.IsZero() {
		b.WriteString(formatWrappedField("created", r.CreatedAt.UTC().Format(time.RFC3339)))
	}
	b.WriteString(formatWrappedField("https", httpsURL))
	b.WriteString(formatWrappedField("ssh", sshURL))
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the repository: %w", err)
	}
	return nil
}
