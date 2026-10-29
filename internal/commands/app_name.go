// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/apps"
)

// An app has a name, the words a person reads for it, apart from its slug,
// its address. The platform's clients rename an app by its name and change
// its address as a separate step, since an address is what other people
// hold; `apps rename` is that separate step here, and `apps name` the
// rename the other clients call Rename.

func newAppNameCmd(apiURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "name <name> [slug]",
		Short: "Change the name people read for an app, keeping its address.",
		Long: `Change an app's name, the words people read for it in the console, in Latere
Chat and in the projects that hold it. The app keeps its slug, its address
and its push URL; 'latere apps rename' changes those.

A name may not be blank. Changing it takes the app's owner, or an owner or
admin of the organization that owns it. Without [slug], the slug is read
from the git remote latere. --json prints the app as the API answers it.`,
		Example: `  latere apps name "Tide tables"
  latere apps name "Latere design boards" latere-clients
  latere apps name "Tide tables" hello --json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return errors.New("an app's name cannot be blank; nothing was sent")
			}
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args[1:]))
			if err != nil {
				return err
			}
			c, err := newAppClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			a, err := c.apps.UpdateApp(cmd.Context(), slug, &apps.UpdateAppParams{Name: name})
			if err != nil {
				return withSlugSource(explainNameRefusal(c.refused(err), slug), slug, fromRemote)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), a.RawJSON())
			}
			return printNamedApp(cmd.OutOrStdout(), a)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the app as JSON")
	return cmd
}

// explainNameRefusal puts what each refusal of a name change names into
// its sentence. Every one of them leaves the app as it was.
func explainNameRefusal(err error, slug string) error {
	e, ok := errors.AsType[*appError](err)
	if !ok {
		return err
	}
	switch e.Code {
	case "invalid_request":
		e.explained = fmt.Sprintf("The platform refused the name of %s, which may not be blank.", slug)
		if field := e.detail("field"); field != "" && field != "name" {
			e.explained = fmt.Sprintf("The platform refused the change's %s.", field)
		}
	case "forbidden":
		e.explained = fmt.Sprintf("You may not change the name of %s.", slug)
		if reason := e.detail("reason"); reason != "" {
			e.explained = fmt.Sprintf("You may not change the name of %s (%s).", slug, reason)
		}
		e.note = "The app's owner can, and so can an owner or admin of the organization that owns it."
	case "app_deleting":
		e.explained = fmt.Sprintf("%s is being deleted, so its name was not changed.", slug)
	}
	return e
}

// printNamedApp writes the app's name beside its slug and address, which
// the change kept.
func printNamedApp(out io.Writer, a *apps.App) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	b.WriteString(formatWrappedField("name", a.Name))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString("\nThe app's address and push URL stay as they were.\n")
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app's name: %w", err)
	}
	return nil
}
