// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// An app's slug is its address and the name of its repository, so a rename
// moves both: the app answers at the new slug, and its push URL carries it.
// The old address is not redirected. A project that pushes to the app holds
// the old push URL in a git remote, and the command moves the one `apps
// create` added when it still names this app.

func newAppRenameCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "rename <new-slug> [slug]",
		Short: "Change an app's address.",
		Long: `Change an app's address by giving the app a new slug. The app and its
previews answer at https://<new-slug>.latere.site at once.

The old address stops working at once, and nothing sends a visitor on from
it to the new one, so links to it stop working. The old slug stays held for
` + appSlugHold + `: nobody else can take it in that time, and renaming the app
back takes it again.

The push URL changes with the slug. In a git repository whose remote latere
is the app's old push URL, the command sets that remote to the new one. A
remote that points anywhere else is left as it is, and the command prints
the line that sets it.

A slug is ` + appSlugGrammar + `,
starting and ending with a letter or a digit. One that breaks a rule is
refused before anything is sent, and
` + invalidSlugReasons[slugRuleShort] + `.
A slug another app has, one held for another account, one the platform
keeps, and the name of another repository of the app's owner are refused.

Renaming takes the app's owner, or an owner or admin of the organization that
owns it. Without [slug], the slug is read from the git remote latere. --json
prints the app as the API answers it, and what became of the remote is said
on standard error.`,
		Example: `  latere apps rename hello-world
  latere apps rename hello-world hello
  latere apps rename hello-world hello --json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			to := args[0]
			if rule := brokenSlugRule(to); rule != "" {
				return fmt.Errorf("%q cannot be an app's slug: %s; nothing was sent", to, invalidSlugReasons[rule])
			}
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args[1:]))
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			// The remote is read before anything is sent, so a repository git
			// cannot read stops the command while the app is as it was.
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("find the current directory: %w", err)
			}
			inRepo := gitRepository(ctx, dir)
			existing, hasRemote := "", false
			if inRepo {
				existing, hasRemote, err = remoteURL(ctx, dir, appRemote)
				if err != nil {
					return err
				}
			}
			c := newAppClient(*apiURL, *authURL)
			// The answer of the rename carries the new push URL alone, and
			// the remote to move is the one that holds the old.
			before, rawBefore, err := c.getApp(ctx, slug)
			if err != nil {
				return withSlugSource(err, slug, fromRemote)
			}
			out, notes := cmd.OutOrStdout(), cmd.OutOrStdout()
			if jsonF {
				notes = cmd.ErrOrStderr()
			}
			// The platform makes no change of a slug that is the app's own,
			// so none is asked for.
			if to == before.Slug {
				if jsonF {
					if err := printRawJSON(out, rawBefore); err != nil {
						return err
					}
				} else if err := printAppAddress(out, before); err != nil {
					return err
				}
				fprintf(notes, "\n%s has this slug already, so nothing changed.\n", before.Slug)
				return nil
			}
			a, raw, err := c.renameApp(ctx, slug, to)
			if err != nil {
				return withSlugSource(err, slug, fromRemote)
			}
			if jsonF {
				if err := printRawJSON(out, raw); err != nil {
					return err
				}
			} else if err := printRenamedApp(out, before, a); err != nil {
				return err
			}
			push := a.Repository.PushURL
			setURL := fmt.Sprintf("git remote set-url %s %s", appRemote, push)
			elsewhere := "In a project that pushes to the app, run:\n  " + setURL
			switch {
			case push == "":
				fprintf(notes, "\nThe platform's answer names no push URL, so no remote was changed. `latere apps show %s` prints it.\n", a.Slug)
			case !inRepo:
				fprintf(notes, "\nThis directory is not a git repository, so no remote was changed. %s\n", elsewhere)
			case !hasRemote:
				fprintf(notes, "\nThis repository has no git remote named %s, so none was changed. %s\n", appRemote, elsewhere)
			case existing == push:
				fprintf(notes, "\nThe git remote %s already points at the new push URL.\n", appRemote)
			case existing == before.Repository.PushURL:
				if _, err := gitIn(ctx, dir, "remote", "set-url", appRemote, push); err != nil {
					return fmt.Errorf("the app was renamed to %s, but setting the git remote %s failed: %w\nSet it with: %s", a.Slug, appRemote, err, setURL)
				}
				fprintf(notes, "\nSet the git remote %s to the new push URL.\n", appRemote)
			default:
				// A remote that names anything but this app's old push URL
				// is another repository's or another app's to keep.
				fprintf(notes, "\nThe git remote %s points at %s, not at this app's old push URL; it was left as it is.\nTo point it at the app, run:\n  %s\n", appRemote, existing, setURL)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the app as JSON")
	return cmd
}

// renameApp gives the app the slug to and answers the app as the change
// leaves it, with the API's own bytes for --json.
func (c *appClient) renameApp(ctx context.Context, slug, to string) (appResource, json.RawMessage, error) {
	raw, err := c.call(ctx, http.MethodPatch, appPath(slug), map[string]string{"slug": to})
	if err != nil {
		return appResource{}, nil, explainRenameRefusal(err, slug, to)
	}
	var a appResource
	if err := json.Unmarshal(raw, &a); err != nil {
		return appResource{}, nil, fmt.Errorf("parse the Apps API's answer: %w", err)
	}
	return a, raw, nil
}

// explainRenameRefusal puts what each refusal of a rename names into its
// sentence. Every one of them leaves the app as it was. A rename is told
// that a slug held for another account is taken, without the hold's end,
// and slug_held of a name the git host still keeps for a repository deleted
// recently; a hold with its end is said as a create says it.
func explainRenameRefusal(err error, from, to string) error {
	e, ok := errors.AsType[*appError](err)
	if !ok {
		return err
	}
	switch e.Code {
	case "slug_taken":
		e.explained = fmt.Sprintf("The slug %s is taken: another app has it, or it is held for the account that gave it up.", defaultStr(e.detail("slug"), to))
	case "slug_held":
		if e.detail("reason") == "repository_name_held" {
			e.explained = fmt.Sprintf("The slug %s is the name of a repository that was deleted recently, and the name is still held.", to)
			break
		}
		explainSlugRefusal(e, to)
	case "repository_name_taken":
		e.explained = fmt.Sprintf("The owner of %s already has a repository named %s, and an app's repository takes the app's slug as its name.", from, to)
	case "repository_unavailable":
		e.explained = fmt.Sprintf("The repository of %s could not be renamed, so nothing was changed. Run the command again in a few minutes.", from)
	case "forbidden":
		e.explained = fmt.Sprintf("You may not rename %s.", from)
		if reason := e.detail("reason"); reason != "" {
			e.explained = fmt.Sprintf("You may not rename %s (%s).", from, reason)
		}
		e.note = "The app's owner can, and so can an owner or admin of the organization that owns it."
	case "invalid_request":
		e.explained = fmt.Sprintf("The platform refused the rename's %s.", defaultStr(e.detail("field"), "body"))
	default:
		explainSlugRefusal(e, to)
	}
	return e
}

// printAppAddress writes where an app answers and where it is pushed to.
func printAppAddress(out io.Writer, a appResource) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString(formatWrappedField("preview", a.PreviewURL))
	b.WriteString(formatWrappedField("push", a.Repository.PushURL))
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app's address: %w", err)
	}
	return nil
}

// printRenamedApp says what a rename left: the app at its new address, that
// the old address stopped and is not redirected, and who the old slug is
// held for, with the command that takes it back.
func printRenamedApp(out io.Writer, before, a appResource) error {
	if err := printAppAddress(out, a); err != nil {
		return err
	}
	holder := "your account"
	if a.Owner.Kind == "organization" {
		holder = "the organization that owns the app"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s stopped working, and the app's previews moved with it. Nothing sends a visitor on to the new address, so links to the old one stop working.\n", defaultStr(before.URL, "The old address"))
	fmt.Fprintf(&b, "The slug %s stays held for %s for %s: nobody else can take it, and `latere apps rename %s %s` takes it back.\n", before.Slug, holder, appSlugHold, before.Slug, a.Slug)
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the renamed app: %w", err)
	}
	return nil
}
