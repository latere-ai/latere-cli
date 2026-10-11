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
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Who can open an app is kept in two places. The app's visibility is a field
// of the app, read and changed at the Apps API. The people invited to a
// private app and its password are the platform's, kept under the app's id,
// which takes a bearer for the platform's own audience: the one the
// repository commands mint, at the address they resolve.

// The values of an app's visibility. A person chooses between private and
// unlisted. Only the platform makes an app public, when it shows the app in
// the gallery, so the commands read that value and never send it.
const (
	visibilityPrivate  = "private"
	visibilityUnlisted = "unlisted"
	visibilityPublic   = "public"
)

// accessRoleViewer is the role every invitation is made with: a viewer opens
// the app at its address and nothing else of it.
const accessRoleViewer = "viewer"

// accessActionSetPassword is the action the platform's answer names where it
// sets and removes app passwords. An answer without it does neither, and
// says of every app that it has no password.
const accessActionSetPassword = "set_password"

// The three choices of who can open an app, in the words every Latere
// surface names them by: a private app without a password, a private app
// with one, and an unlisted or public app.
const (
	choiceInvited  = "Only people invited"
	choicePassword = "Anyone with the password"
	choiceLink     = "Anyone with the link"
)

// utcMinute is how a moment is written for a person: to the minute, in UTC.
const utcMinute = "2006-01-02 15:04 UTC"

// checkVisibility refuses, before any request, a value a person may not
// choose: public with how an app becomes public, anything else with the two
// choices.
func checkVisibility(v string) error {
	switch v {
	case visibilityPrivate, visibilityUnlisted:
		return nil
	case visibilityPublic:
		return fmt.Errorf("%s cannot be chosen here: an app becomes public when it is shown in the gallery, which you ask for from the app's Share in the chat and which is reviewed first", visibilityPublic)
	}
	return fmt.Errorf("%q is not a visibility; choose %s or %s", v, visibilityPrivate, visibilityUnlisted)
}

// whoSignsIn names the people a private app opens for once they sign in, by
// whether an organization owns it.
func whoSignsIn(a appResource) string {
	if a.Owner.Kind == "organization" {
		return "the members of its organization and the people invited"
	}
	return "you and the people you invite"
}

// whoOpens says who can open an app, by the visibility its answer carries
// and whether an organization owns it, as a clause about the app. It is for
// a command that does not read the platform, so it says nothing of a
// password. A visibility this version does not know has none.
func whoOpens(a appResource) string {
	switch a.Visibility {
	case visibilityPrivate:
		return "only " + whoSignsIn(a) + " can open it, each after signing in with a Latere account"
	case visibilityUnlisted:
		return "anyone who has its address can open it without signing in, and it is kept out of search engines"
	case visibilityPublic:
		return "anyone can open it, and search engines may list it"
	}
	return ""
}

// whoOpensSentence is whoOpens as a sentence of its own, "" when there is
// none.
func whoOpensSentence(a appResource) string {
	who := whoOpens(a)
	if who == "" {
		return ""
	}
	return upperFirst(who) + "."
}

// upperFirst is s with its first letter, an ASCII one, in upper case.
func upperFirst(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
}

// opensToAnyone reports whether the app's visibility lets in anyone who has
// its address, so that an invitation or a password decides nothing while it
// lasts.
func opensToAnyone(a appResource) bool {
	return a.Visibility == visibilityUnlisted || a.Visibility == visibilityPublic
}

// generalAccess says who can open an app as one of the three choices, then
// what the choice lets in, each sentence on a line of its own. password is
// the app's password as the platform answers it, nil when it has none; one
// without a moment is one the command just set. A password left on an app
// anyone with the link opens gets lines of its own, with the command that
// removes it. A visibility this version does not know has no sentence.
func generalAccess(a appResource, password *appPassword) string {
	var listing string
	switch a.Visibility {
	case visibilityPrivate:
		if password == nil {
			return fmt.Sprintf("%s: %s open it, each after signing in with a Latere account.\n", choiceInvited, whoSignsIn(a))
		}
		return fmt.Sprintf("%s%s: whoever types it at the app's address opens the app, with no Latere account.\n%s still open the app by signing in with a Latere account.\n",
			choicePassword, password.since(", set "), upperFirst(whoSignsIn(a)))
	case visibilityUnlisted:
		listing = "is kept out of search engines"
	case visibilityPublic:
		listing = "search engines may list it"
	default:
		return ""
	}
	who := fmt.Sprintf("%s: it opens without signing in, and %s.\n", choiceLink, listing)
	if password != nil {
		who += fmt.Sprintf("A password is set%s. It decides nothing while anyone with the link opens the app, and opens the app again once it is private.\nRemove it with: latere apps password --remove %s\n",
			password.since(" since "), a.Slug)
	}
	return who
}

// ---- the wire ----

// appAccess is what the platform keeps of who can open an app: the people
// invited, the password when one is set, what the caller may do, and the
// bounds of a password.
type appAccess struct {
	People   []appAccessPerson `json:"people"`
	Password *appPassword      `json:"password"`
	Actions  []string          `json:"actions"`
	// Limits are the fewest and the most characters a password has. A
	// platform that names neither leaves both zero.
	Limits struct {
		MinPassword int `json:"min_password"`
		MaxPassword int `json:"max_password"`
	} `json:"limits"`
}

// appPassword is what the platform says of a password that is set: when, and
// nothing of the password.
type appPassword struct {
	SetAt time.Time `json:"set_at"`
}

// since is the moment the password was set, after lead, or "" for a password
// whose moment is not known.
func (p *appPassword) since(lead string) string {
	if p.SetAt.IsZero() {
		return ""
	}
	return lead + p.SetAt.UTC().Format(utcMinute)
}

// offersPasswords reports whether the platform sets and removes app
// passwords for the caller.
func (a appAccess) offersPasswords() bool {
	return slices.Contains(a.Actions, accessActionSetPassword)
}

// appAccessPerson is one invitation: pending until a person signs in at the
// app with the invited address, active from then on, with their name.
type appAccessPerson struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	State  string `json:"state"`
	Person *struct {
		DisplayName string `json:"display_name"`
		Handle      string `json:"handle"`
	} `json:"person"`
	InvitedAt time.Time `json:"invited_at"`
}

// appInvites is the answer of an invite: the invitations it made, and the
// addresses it left out with the reason for each.
type appInvites struct {
	People  []appAccessPerson `json:"people"`
	Skipped []struct {
		Email  string `json:"email"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

// skipReasons says why an invite left an address out, by the reason the
// platform names.
var skipReasons = map[string]string{
	"self":   "your own address, and you open the app already",
	"listed": "already invited",
}

// accessPath is the platform's path of who can open the app whose id is
// given, then each segment of rest, every one escaped.
func accessPath(appID string, rest ...string) string {
	parts := []string{"/v1/account/apps", url.PathEscape(appID), "access"}
	for _, r := range rest {
		parts = append(parts, url.PathEscape(r))
	}
	return strings.Join(parts, "/")
}

// appForAccess reads the app a command that asks the platform acts on. The
// platform keeps the people invited and the password under the app's id,
// which only the app's answer names.
func appForAccess(cmd *cobra.Command, c *appClient, arg string) (appResource, json.RawMessage, error) {
	slug, fromRemote, err := resolveAppSlug(cmd, arg)
	if err != nil {
		return appResource{}, nil, err
	}
	return readAppForAccess(cmd.Context(), c, slug, fromRemote)
}

// readAppForAccess is appForAccess for a slug already resolved, with where
// it came from.
func readAppForAccess(ctx context.Context, c *appClient, slug string, fromRemote bool) (appResource, json.RawMessage, error) {
	a, raw, err := c.getApp(ctx, slug)
	if err != nil {
		return appResource{}, nil, withSlugSource(err, slug, fromRemote)
	}
	if a.ID == "" {
		return appResource{}, nil, fmt.Errorf("the Apps API's answer names no id for %s, and the people invited and the password are kept under it", slug)
	}
	return a, raw, nil
}

// readAccess reads the people invited to the app and whether it has a
// password, keeping the raw bytes for --json.
func readAccess(ctx context.Context, platformURL, authURL string, a appResource) (appAccess, json.RawMessage, error) {
	var raw json.RawMessage
	if err := platformCall(ctx, platformURL, authURL, http.MethodGet, accessPath(a.ID), nil, &raw); err != nil {
		return appAccess{}, nil, explainAccessRefusal(err, a.Slug, nil)
	}
	var access appAccess
	if err := json.Unmarshal(raw, &access); err != nil {
		return appAccess{}, nil, fmt.Errorf("parse the platform's answer: %w", err)
	}
	return access, raw, nil
}

// shareArgs splits the arguments of share and unshare into the slug, "" when
// it is left out, and the addresses. A slug never holds an @, so an argument
// without one is the slug in first position and a mistake anywhere else.
func shareArgs(cmd *cobra.Command, args []string) (slug string, emails []string, err error) {
	usage := strings.TrimSuffix(cmd.CommandPath(), cmd.Name()) + cmd.Use
	if len(args) > 0 && !strings.Contains(args[0], "@") {
		slug, args = args[0], args[1:]
	}
	if len(args) == 0 {
		return "", nil, fmt.Errorf("name at least one email address: %s", usage)
	}
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if !strings.Contains(arg, "@") {
			return "", nil, fmt.Errorf("%q is not an email address, and the slug comes first: %s", arg, usage)
		}
		emails = append(emails, arg)
	}
	return slug, emails, nil
}

// setVisibility changes the app's visibility and answers the app as the
// change leaves it, with the API's own bytes for --json.
func (c *appClient) setVisibility(ctx context.Context, slug, visibility string) (appResource, json.RawMessage, error) {
	raw, err := c.call(ctx, http.MethodPatch, appPath(slug), map[string]string{"visibility": visibility})
	if err != nil {
		return appResource{}, nil, explainVisibilityRefusal(err, slug)
	}
	var a appResource
	if err := json.Unmarshal(raw, &a); err != nil {
		return appResource{}, nil, fmt.Errorf("parse the Apps API's answer: %w", err)
	}
	return a, raw, nil
}

// explainVisibilityRefusal puts what a refusal of a visibility change names
// into its sentence.
func explainVisibilityRefusal(err error, slug string) error {
	e, ok := errors.AsType[*appError](err)
	if !ok {
		return err
	}
	switch e.Code {
	case "forbidden":
		e.explained = fmt.Sprintf("You may not change who can open %s.", slug)
		if reason := e.detail("reason"); reason != "" {
			e.explained = fmt.Sprintf("You may not change who can open %s (%s).", slug, reason)
		}
		e.note = "The app's owner can, and so can an owner or admin of the organization that owns it."
	case "invalid_request":
		e.explained = fmt.Sprintf("The platform refused the change's %s.", defaultStr(e.detail("field"), "body"))
	}
	return e
}

// passwordsUnconfigured is the code of a platform that sets and removes no
// app passwords, and passwordsUnavailable what a command says of it. The
// platform answers the code on the routes of a password; a command that
// reads no set_password among the actions says the same before it sends
// anything.
const (
	passwordsUnconfigured = "passwords_unconfigured"
	passwordsUnavailable  = "Passwords are not available on this platform yet."
)

// explainAccessRefusal puts what a refusal of an app's access list or of its
// password names into its sentence: the address that is not one, the bound
// an invite met, the bounds of a password. emails are the addresses an
// invite sent, in the order it sent them. An invite is made whole or not at
// all, so each of its refusals says no one was invited.
func explainAccessRefusal(err error, slug string, emails []string) error {
	e, ok := errors.AsType[*platformError](err)
	if !ok {
		return err
	}
	switch e.Code {
	case "access_not_found":
		e.Message = fmt.Sprintf("The platform found no app %s in your current context.", slug)
	case passwordsUnconfigured:
		e.Message = passwordsUnavailable
	case "invalid_field":
		if i, ok := e.Details["index"].(float64); ok && i >= 0 && int(i) < len(emails) {
			e.Message = fmt.Sprintf("%s is not an email address. No one was invited.", emails[int(i)])
			break
		}
		switch e.Details["field"] {
		case "emails":
			e.Message = "The platform refused the list of addresses. No one was invited."
		case "password":
			e.Message = "The platform refused the password. The app's password was not changed."
			fewest, okMin := e.Details["min"].(float64)
			most, okMax := e.Details["max"].(float64)
			if e.Details["reason"] == "length" && okMin && okMax {
				e.Message = fmt.Sprintf("The platform refused the password: a password is %g to %g characters. The app's password was not changed.", fewest, most)
			}
		}
	case "invite_limit":
		bound := ""
		if most, ok := e.Details["max"].(float64); ok {
			bound = fmt.Sprintf(" (%g)", most)
		}
		switch e.Details["limit"] {
		case "people":
			e.Message = fmt.Sprintf("These invitations would give %s more people invited than an app can have%s. No one was invited. `latere apps unshare` takes an invitation back.", slug, bound)
		case "per_day":
			e.Message = fmt.Sprintf("You have made as many invitations as one person can for now%s. No one was invited.", bound)
			if wait, ok := e.Details["retry_after"].(float64); ok && wait > 0 {
				e.Message += " You can invite again from " + time.Now().Add(time.Duration(wait)*time.Second).UTC().Format(utcMinute) + "."
			}
		}
	}
	return wrapReposErr(e)
}

// ---- visibility ----

func newAppVisibilityCmd(apiURL, authURL *string) *cobra.Command {
	var (
		platformURL string
		jsonF       bool
	)
	cmd := &cobra.Command{
		Use:   "visibility <private|unlisted> [slug]",
		Short: "Change who can open an app.",
		Long: `Change who can open an app at its address and at its previews.

  private    only people invited: you, or the members of the organization
             that owns the app, and the people you invite, each after
             signing in with a Latere account
  unlisted   anyone with the link, without signing in; the app is kept out
             of search engines

Both remove the app's password when it has one, and the command says so.
Private means only people invited, so the people who used the password no
longer get in. An unlisted app opens for anyone with the link, where a
password decides nothing, and one left set would open the app again once it
is private. The third choice, anyone with the password, is 'latere apps
password'.

An app becomes public, which lets search engines list it, when it is shown in
the gallery at latere.site. You ask for that from the app's Share in the
chat, and it is reviewed first. Changing the visibility takes the app out of
the gallery, and ends a request to show it there that still waits for its
review.

The people you invited stay invited whatever the visibility is, and open the
app again once it is private.

Changing it takes the app's owner, or an owner or admin of the organization
that owns it. Without [slug], the slug is read from the git remote latere.
--json prints the app as the API answers it, and a password that was removed
is said on standard error.`,
		Example: `  latere apps visibility unlisted
  latere apps visibility private hello
  latere apps visibility unlisted hello --json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkVisibility(args[0]); err != nil {
				return err
			}
			ctx := cmd.Context()
			c := newAppClient(*apiURL, *authURL)
			// The answer of the change carries the new visibility alone, and
			// what the change ends depends on the one before it.
			before, _, err := appForAccess(cmd, c, firstArg(args[1:]))
			if err != nil {
				return err
			}
			// Whether the app has a password is read before anything changes,
			// so a platform that does not say leaves the app as it was.
			access, _, err := readAccess(ctx, platformURL, *authURL, before)
			if err != nil {
				return fmt.Errorf("%s was left as it is, since whether it has a password could not be read: %w", before.Slug, err)
			}
			a, raw, err := c.setVisibility(ctx, before.Slug, args[0])
			if err != nil {
				return err
			}
			// Neither value is the choice a password makes, so a password
			// goes with the change. One that stays is still the app's, and
			// is said with the failure.
			left, removed := access.Password, false
			var failed error
			if access.offersPasswords() && left != nil {
				if err := platformCall(ctx, platformURL, *authURL, http.MethodDelete, accessPath(before.ID, "password"), nil, nil); err != nil {
					failed = fmt.Errorf("%s is %s now, and its password is still set: removing it failed: %w", a.Slug, a.Visibility, explainAccessRefusal(err, a.Slug, nil))
				} else {
					left, removed = nil, true
				}
			}
			if jsonF {
				if err := printRawJSON(cmd.OutOrStdout(), raw); err != nil {
					return err
				}
				if removed {
					fprintf(cmd.ErrOrStderr(), "Removed the password of %s. %s\n", a.Slug, passwordGone(a))
				}
				return failed
			}
			if err := printVisibility(cmd.OutOrStdout(), before, a, left, removed); err != nil {
				return err
			}
			return failed
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the app as JSON")
	return cmd
}

// leftGallery says what a change to unlisted or private ended in the
// gallery, by the visibility before it: the card of a public app, or the
// request an unlisted app had waiting for review. "" for a private app,
// which had neither.
func leftGallery(before string) string {
	switch before {
	case visibilityPublic:
		return "If it was shown in the gallery, it has left the gallery."
	case visibilityUnlisted:
		return "If you had asked to show it in the gallery, that request has ended."
	}
	return ""
}

// passwordGone says what the removal of a password changes, for the app as
// it is now.
func passwordGone(a appResource) string {
	if opensToAnyone(a) {
		return "Anyone with the link opens the app, where a password decides nothing, and one left set would open the app again once it is private."
	}
	return "The people who used it no longer get in, and a visit made with it ends within a minute."
}

// printVisibility says what a change of visibility left: the app, who can
// open it, what the change ended, and whether the app's password went with
// it. left is the password the app still has, nil for none.
func printVisibility(out io.Writer, before, a appResource, left *appPassword, removed bool) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString(formatWrappedField("visibility", a.Visibility))
	if who := generalAccess(a, left); who != "" {
		b.WriteString("\n" + who)
	}
	switch before.Visibility {
	case a.Visibility:
		if removed {
			fmt.Fprintf(&b, "It was %s already.\n", a.Visibility)
		} else {
			fmt.Fprintf(&b, "It was %s already, so nothing changed.\n", a.Visibility)
		}
	case visibilityPublic:
		fmt.Fprintf(&b, "It was public before. %s\n", leftGallery(before.Visibility))
	case visibilityUnlisted:
		fmt.Fprintf(&b, "%s\n", leftGallery(before.Visibility))
	}
	if removed {
		fmt.Fprintf(&b, "Its password was removed. %s\n", passwordGone(a))
	}
	if a.Visibility == visibilityPrivate {
		fmt.Fprintf(&b, "Invite people by email with: latere apps share %s <email>...\n", a.Slug)
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app's visibility: %w", err)
	}
	return nil
}

// ---- access ----

func newAppAccessCmd(apiURL, authURL *string) *cobra.Command {
	var (
		platformURL string
		jsonF       bool
	)
	cmd := &cobra.Command{
		Use:   "access [slug]",
		Short: "Show who can open an app, whether it has a password, and the people invited to it.",
		Long: `Show who can open an app: which of the three choices it is on, then the
people invited to it by email.

  Only people invited        a private app: you, or the members of the
                             organization that owns it, and the people you
                             invite, each after signing in
  Anyone with the password   a private app with a password: also whoever
                             types the password, with no Latere account
  Anyone with the link       an unlisted or public app

A password is said with the moment it was set, and never the password
itself: the platform keeps none it can show again. 'latere apps password'
sets, replaces and removes it.

The people invited are listed one line each: the address, whether the
invitation is pending or active, the person's name once they accepted it,
and how long ago they were invited.

An invitation is pending until the person opens the app and signs in with a
Latere account whose verified email is the invited address. From then on it
is active, and the app opens for that account.

The people invited open the app while it is private. They stay invited while
it is unlisted or public, and open it again once it is private.

The list is shown to whoever may change the app's settings: its owner, or an
owner or admin of the organization that owns it. Without [slug], the slug is
read from the git remote latere. --json prints an object with the app and
its access list, each as its API answers it; the list's "password" is when
one was set, or null.`,
		Example: `  latere apps access
  latere apps access hello
  latere apps access hello --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, rawApp, err := appForAccess(cmd, newAppClient(*apiURL, *authURL), firstArg(args))
			if err != nil {
				return err
			}
			access, rawAccess, err := readAccess(cmd.Context(), platformURL, *authURL, a)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, struct {
					App    json.RawMessage `json:"app"`
					Access json.RawMessage `json:"access"`
				}{App: rawApp, Access: rawAccess})
			}
			return printAccess(out, a, access)
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func printAccess(out io.Writer, a appResource, access appAccess) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString(formatWrappedField("visibility", a.Visibility))
	if who := generalAccess(a, access.Password); who != "" {
		b.WriteString("\n" + who)
	}
	if len(access.People) == 0 {
		fmt.Fprintf(&b, "\nNo one is invited. Invite people by email with: latere apps share %s <email>...\n", a.Slug)
	} else {
		if opensToAnyone(a) {
			b.WriteString("The people invited open it once it is private.\n")
		}
		b.WriteString("\n")
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "EMAIL\tSTATE\tNAME\tINVITED")
		for _, p := range access.People {
			name := ""
			if p.Person != nil {
				name = defaultStr(p.Person.DisplayName, p.Person.Handle)
			}
			fprintf(w, "%s\t%s\t%s\t%s\n", p.Email, p.State, defaultStr(name, "-"), humanAge(p.InvitedAt))
		}
		_ = w.Flush()
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app's access: %w", err)
	}
	return nil
}

// ---- share ----

func newAppShareCmd(apiURL, authURL *string) *cobra.Command {
	var (
		platformURL string
		jsonF       bool
	)
	cmd := &cobra.Command{
		Use:   "share [slug] <email>...",
		Short: "Invite people by email to open a private app.",
		Long: `Invite people to open a private app, by their email address. Each person
invited opens the app at its address and at its previews, and sees nothing
else of it.

Invitations are not emailed: send the app's address to the people you
invited. Each of them opens it and signs in with a Latere account whose
verified email is the address you invited, or creates one with that address.
The first sign-in with that address accepts the invitation, and from then on
the app opens for that account.

An argument without an @ in first position is the slug, and every other
argument is an address. Without a slug, it is read from the git remote
latere. Your own address and an address already invited are skipped, and the
command says so.

An unlisted or public app is already open to anyone who has its address. The
people invited to one open it once it is private.

Inviting takes whoever may change the app's settings: its owner, or an owner
or admin of the organization that owns it. --json prints the invitations as
the platform answers them.`,
		Example: `  latere apps share ada@example.com
  latere apps share hello ada@example.com grace@example.com
  latere apps share hello ada@example.com --json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slugArg, emails, err := shareArgs(cmd, args)
			if err != nil {
				return err
			}
			a, _, err := appForAccess(cmd, newAppClient(*apiURL, *authURL), slugArg)
			if err != nil {
				return err
			}
			var raw json.RawMessage
			body := map[string]any{"emails": emails, "role": accessRoleViewer}
			if err := platformCall(cmd.Context(), platformURL, *authURL, http.MethodPost, accessPath(a.ID, "invites"), body, &raw); err != nil {
				return explainAccessRefusal(err, a.Slug, emails)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printRawJSON(out, raw)
			}
			var invited appInvites
			if err := json.Unmarshal(raw, &invited); err != nil {
				return fmt.Errorf("parse the platform's answer: %w", err)
			}
			return printInvited(out, a, invited)
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the invitations as JSON")
	return cmd
}

func printInvited(out io.Writer, a appResource, invited appInvites) error {
	var b strings.Builder
	if len(invited.People) == 0 {
		fmt.Fprintf(&b, "No one new was invited to %s.\n", a.Slug)
	} else {
		fmt.Fprintf(&b, "Invited to %s:\n", a.Slug)
		for _, p := range invited.People {
			fmt.Fprintf(&b, "  %s\n", p.Email)
		}
	}
	if len(invited.Skipped) > 0 {
		b.WriteString("Skipped:\n")
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		for _, s := range invited.Skipped {
			fprintf(w, "  %s\t%s\n", s.Email, defaultStr(skipReasons[s.Reason], s.Reason))
		}
		_ = w.Flush()
	}
	if len(invited.People) > 0 {
		fmt.Fprintf(&b, "\nInvitations are not emailed. Send the people you invited the app's address:\n  %s\nEach of them opens it after signing in with a Latere account whose verified email is the address you invited.\n", a.URL)
	}
	if opensToAnyone(a) {
		fmt.Fprintf(&b, "\nThe app is %s, so it is already open to anyone who has its address. The invitations decide who opens it once it is private.\n", a.Visibility)
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the invitations: %w", err)
	}
	return nil
}

// ---- unshare ----

func newAppUnshareCmd(apiURL, authURL *string) *cobra.Command {
	var platformURL string
	cmd := &cobra.Command{
		Use:   "unshare [slug] <email>...",
		Short: "Take back invitations to an app, by email.",
		Long: `Take back invitations to an app, each by the address that was invited.

An invitation no one accepted is gone at once. A person who already opened
the app is refused shortly after, at a later visit. An address that is not
invited is reported, and the others are still taken back.

An argument without an @ in first position is the slug, and every other
argument is an address. Without a slug, it is read from the git remote
latere.

Taking an invitation back takes whoever may change the app's settings: its
owner, or an owner or admin of the organization that owns it.`,
		Example: `  latere apps unshare ada@example.com
  latere apps unshare hello ada@example.com grace@example.com`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slugArg, emails, err := shareArgs(cmd, args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			a, _, err := appForAccess(cmd, newAppClient(*apiURL, *authURL), slugArg)
			if err != nil {
				return err
			}
			access, _, err := readAccess(ctx, platformURL, *authURL, a)
			if err != nil {
				return err
			}
			// An invitation is removed by its id, which the list names for
			// each address, compared without case as the platform keeps them.
			var removed, absent []string
			var failed error
			for _, email := range emails {
				id := ""
				for _, p := range access.People {
					if strings.EqualFold(p.Email, email) {
						id = p.ID
						break
					}
				}
				if id == "" {
					absent = append(absent, email)
					continue
				}
				if err := platformCall(ctx, platformURL, *authURL, http.MethodDelete, accessPath(a.ID, id), nil, nil); err != nil {
					failed = fmt.Errorf("take back the invitation of %s: %w", email, explainAccessRefusal(err, a.Slug, nil))
					break
				}
				removed = append(removed, email)
			}
			// What was taken back before a failure is said before the
			// failure, so a second run names only what is left.
			if err := printUninvited(cmd.OutOrStdout(), a, removed, absent); err != nil {
				return err
			}
			return failed
		},
	}
	cmd.Flags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	return cmd
}

func printUninvited(out io.Writer, a appResource, removed, absent []string) error {
	var b strings.Builder
	if len(removed) > 0 {
		fmt.Fprintf(&b, "No longer invited to %s:\n", a.Slug)
		for _, email := range removed {
			fmt.Fprintf(&b, "  %s\n", email)
		}
	}
	if len(absent) > 0 {
		fmt.Fprintf(&b, "Not invited to %s, so nothing to take back:\n", a.Slug)
		for _, email := range absent {
			fmt.Fprintf(&b, "  %s\n", email)
		}
	}
	if len(removed) > 0 {
		b.WriteString("\nAn invitation no one accepted is gone at once. A person who already opened the app is refused shortly after.\n")
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the invitations taken back: %w", err)
	}
	return nil
}
