// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// A password opens a private app for anyone who types it at the app's
// address, with no Latere account. The platform keeps it under the app's id
// beside the people invited, in a form it cannot read back, so the command
// that sets one is the only place it is ever in the clear: it is read from a
// prompt, from standard input or made here, sent once, and printed only when
// it was made here.

// The shape of a generated password: groups of characters from an alphabet
// without the ones that look alike (i, l, o, 0 and 1), joined by hyphens.
const (
	passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	passwordGroups   = 4
	passwordGroupLen = 5
)

// maxPasswordLine bounds the line --stdin reads. It is far above any
// password a platform takes, so a file piped by mistake is not read whole.
const maxPasswordLine = 64 << 10

// typedSecret answers the reader of one line typed without echo at the
// terminal in is, or nil when in is no terminal. A variable so a test can
// stand in for a terminal.
var typedSecret = func(in io.Reader) func() (string, error) {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	return func() (string, error) {
		line, err := term.ReadPassword(int(f.Fd()))
		return string(line), err
	}
}

func newAppPasswordCmd(apiURL, authURL *string) *cobra.Command {
	var (
		platformURL                        string
		generate, fromStdin, remove, jsonF bool
	)
	cmd := &cobra.Command{
		Use:   "password [slug]",
		Short: "Set, replace or remove the password that opens a private app.",
		Long: `Set the password of an app, or replace the one it has. Whoever opens the
app's address or one of its previews is asked for the password, and whoever
types it opens the app, with no Latere account. You, the members of the
organization that owns the app and the people invited still open it by
signing in.

A password is for a private app, so setting one makes the app private when
it is not: an app that anyone with the link opened then opens with the
password, or by signing in. Replacing a password ends every visit made with
the one before within about a minute.

A password is never a flag's value or an argument, which a shell history
would keep. It comes from the first of these that applies:

  --generate   the command makes one: 20 letters and digits in four groups,
               without the ones that look alike
  --stdin      one line of standard input, without its line ending and with
               nothing else removed, so a space is part of the password
  a prompt     on a terminal, typed twice without being shown

A generated password is printed once, on the last line of the output. The
platform keeps no password in a form it can show again, so copy it then. A
lost password is replaced, never recovered. The platform says how short and
how long a password may be, and the command holds one to that before it
sends it.

--remove removes the password and leaves the app's visibility as it is: the
people who used the password no longer get in, and a private app opens only
for the people invited. It is no error when the app has none. 'latere apps
visibility' removes a password too, with either of its values.

Nothing is emailed: send people the app's address and the password
yourself. 'latere apps access' says whether a password is set, and since
when. Where the platform does not offer passwords yet, the command says so
and changes nothing.

All of it takes whoever may change the app's settings: its owner, or an
owner or admin of the organization that owns it. Without [slug], the slug is
read from the git remote latere. --json prints an object with the app as the
API answers it and "password": whether one is set, and the generated value
when --generate made it. A password you typed or piped is in no output.`,
		Example: `  latere apps password
  latere apps password hello --generate
  latere apps password hello --stdin < password.txt
  latere apps password hello --generate --json
  latere apps password hello --remove`,
		Args: func(cmd *cobra.Command, args []string) error {
			// The refusal names no argument: one of them may be a password.
			if len(args) > 1 {
				return fmt.Errorf("%s takes the slug alone: a password is never an argument, which a shell history would keep. It is typed at a prompt, read with --stdin, or made with --generate", cmd.CommandPath())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if remove && (generate || fromStdin) {
				return errors.New("--remove sets no password, so it takes neither --generate nor --stdin")
			}
			prompted := !remove && !generate && !fromStdin
			typed := typedSecret(cmd.InOrStdin())
			if prompted && typed == nil {
				return errors.New("there is no terminal to type the password at: pass --stdin to read it from standard input, or --generate to have one made")
			}
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args))
			if err != nil {
				return err
			}
			// A typed password is asked for before any request. The terminal
			// shows what is typed until the prompt's read turns that off, so
			// a prompt that waited for the platform would show whatever a
			// person typed ahead of it.
			var password string
			if prompted {
				if password, err = promptPassword(cmd.ErrOrStderr(), typed, slug); err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			c := newAppClient(*apiURL, *authURL)
			a, rawApp, err := readAppForAccess(ctx, c, slug, fromRemote)
			if err != nil {
				return err
			}
			// The platform says whether it takes passwords at all before the
			// app is touched: making an app private for a password that
			// cannot be set would close it to everyone but the people invited.
			access, _, err := readAccess(ctx, platformURL, *authURL, a)
			if err != nil {
				return err
			}
			if !access.offersPasswords() {
				return &platformError{Code: passwordsUnconfigured, Message: passwordsUnavailable + " Nothing was changed."}
			}
			out := cmd.OutOrStdout()
			if remove {
				had := access.Password != nil
				if had {
					if err := platformCall(ctx, platformURL, *authURL, http.MethodDelete, accessPath(a.ID, "password"), nil, nil); err != nil {
						return explainAccessRefusal(err, a.Slug, nil)
					}
				}
				if jsonF {
					return printJSON(out, passwordResult(rawApp, false, ""))
				}
				return printPasswordRemoved(out, a, had)
			}

			switch {
			case generate:
				password, err = generatePassword()
			case fromStdin:
				password, err = readPasswordLine(cmd.InOrStdin())
			}
			if err != nil {
				return err
			}
			if err := checkPassword(password, access); err != nil {
				return err
			}
			after, madePrivate := a, a.Visibility != visibilityPrivate
			if madePrivate {
				if after, rawApp, err = c.setVisibility(ctx, a.Slug, visibilityPrivate); err != nil {
					return err
				}
			}
			if err := putPassword(ctx, platformURL, *authURL, a, password); err != nil {
				if madePrivate {
					return fmt.Errorf("%w\n%s was made private before the password was sent, and stays private. `latere apps access %s` says whether a password is set, and `latere apps visibility %s %s` opens the app to anyone with the link again", err, a.Slug, a.Slug, visibilityUnlisted, a.Slug)
				}
				return err
			}
			if !generate {
				password = ""
			}
			if jsonF {
				return printJSON(out, passwordResult(rawApp, true, password))
			}
			return printPasswordSet(out, a, after, access.Password != nil, password)
		},
	}
	// A flag that cannot be read is not repeated either: pflag's own refusal
	// quotes what it was given, and that may be a password.
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return fmt.Errorf("a flag given to %s could not be read, and is not repeated here in case it holds a password. A password is never a flag's value: `%s --help` lists the flags", cmd.CommandPath(), cmd.CommandPath())
	})
	f := cmd.Flags()
	f.BoolVar(&generate, "generate", false, "make a password and print it once")
	f.BoolVar(&fromStdin, "stdin", false, "read the password from one line of standard input")
	f.BoolVar(&remove, "remove", false, "remove the app's password")
	f.StringVar(&platformURL, "platform-url", "", platformURLUsage)
	f.BoolVar(&jsonF, "json", false, "print the app and whether it has a password as JSON")
	return cmd
}

// generatePassword makes a password of passwordGroups groups of
// passwordGroupLen characters. Each character is drawn by rand.Int, which
// rejects what a remainder would favor, so every one of the alphabet is as
// likely as another.
func generatePassword() (string, error) {
	size := big.NewInt(int64(len(passwordAlphabet)))
	var b strings.Builder
	for i := range passwordGroups * passwordGroupLen {
		if i > 0 && i%passwordGroupLen == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, size)
		if err != nil {
			return "", fmt.Errorf("generate a password: %w", err)
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// readPasswordLine reads the password --stdin names: the first line of in
// without its line ending, LF or CRLF, and with nothing else removed.
func readPasswordLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(in, maxPasswordLine)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the password from standard input: %w", err)
	}
	if rest, ok := strings.CutSuffix(line, "\n"); ok {
		line = strings.TrimSuffix(rest, "\r")
	}
	if line == "" {
		return "", errors.New("--stdin read no password: the first line of standard input is empty. Nothing was changed")
	}
	return line, nil
}

// promptPassword asks for the password of slug twice on prompts, reading
// each answer with typed, and refuses two answers that differ: nothing
// typed is shown, so a slip in one would set a password no one knows.
func promptPassword(prompts io.Writer, typed func() (string, error), slug string) (string, error) {
	var answers [2]string
	for i, prompt := range []string{"Password for " + slug + ": ", "Type it again: "} {
		fprintf(prompts, "%s", prompt)
		answer, err := typed()
		// The line the person ended was not echoed either.
		fprintln(prompts)
		if err != nil {
			return "", fmt.Errorf("read the password: %w", err)
		}
		answers[i] = answer
	}
	if answers[0] != answers[1] {
		return "", errors.New("the two passwords typed are not the same. Nothing was changed")
	}
	return answers[0], nil
}

// checkPassword refuses, before any request carries it, a password the
// platform would refuse: one outside the bounds the access answer names,
// counted in Unicode code points as the platform counts them. A bound the
// answer leaves out is the platform's to hold. Text that is not UTF-8 is
// refused as well: it would be sent as other characters than were given,
// and no one could type the result.
func checkPassword(password string, access appAccess) error {
	if !utf8.ValidString(password) {
		return errors.New("the password is not UTF-8 text, so it could not be typed again as it was given. Nothing was changed")
	}
	n, fewest, most := utf8.RuneCountInString(password), access.Limits.MinPassword, access.Limits.MaxPassword
	if fewest > 0 && n < fewest {
		return fmt.Errorf("a password is at least %d characters, and this one is shorter. Nothing was changed", fewest)
	}
	if most > 0 && n > most {
		return fmt.Errorf("a password is at most %d characters, and this one is longer. Nothing was changed", most)
	}
	return nil
}

// putPassword sets the app's password at the platform, or replaces it.
func putPassword(ctx context.Context, platformURL, authURL string, a appResource, password string) error {
	err := platformCall(ctx, platformURL, authURL, http.MethodPut, accessPath(a.ID, "password"), map[string]string{"password": password}, nil)
	if err == nil {
		return nil
	}
	// An answer that is no refusal of the platform's, such as a proxy's
	// page, may repeat the request it was sent, so none of its text is kept.
	if e, ok := errors.AsType[*platformError](err); ok && e.Code == "" {
		e.Message, e.Detail = "", ""
	}
	return explainAccessRefusal(err, a.Slug, nil)
}

// passwordResult is what --json prints: the app as the API answers it,
// whether it has a password now, and the value when the command made it.
func passwordResult(app json.RawMessage, set bool, generated string) any {
	type state struct {
		Set       bool   `json:"set"`
		Generated string `json:"generated,omitempty"`
	}
	return struct {
		App      json.RawMessage `json:"app"`
		Password state           `json:"password"`
	}{App: app, Password: state{Set: set, Generated: generated}}
}

// printPasswordSet says that the password is set: the app, who can open it
// now, what setting it changed, and last the password when the command made
// it, alone on its line. before is the app as it was, and replaced whether
// it had a password already.
func printPasswordSet(out io.Writer, before, a appResource, replaced bool, generated string) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("app", a.Slug))
	b.WriteString(formatWrappedField("address", a.URL))
	b.WriteString(formatWrappedField("visibility", a.Visibility))
	if replaced {
		fmt.Fprintf(&b, "\nThe password of %s is replaced. The one before stops opening the app within about a minute.\n", a.Slug)
	} else {
		fmt.Fprintf(&b, "\nThe password of %s is set.\n", a.Slug)
	}
	b.WriteString(generalAccess(a, &appPassword{}))
	if before.Visibility != a.Visibility {
		fmt.Fprintf(&b, "The app was %s before and is %s now, so its address alone no longer opens it.\n", before.Visibility, a.Visibility)
		if ended := leftGallery(before.Visibility); ended != "" {
			b.WriteString(ended + "\n")
		}
	}
	b.WriteString("Send people the app's address and the password. Neither is emailed.\n")
	if generated != "" {
		fmt.Fprintf(&b, "\nThe generated password, printed this once:\n%s\n", generated)
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app's password: %w", err)
	}
	return nil
}

// printPasswordRemoved says that the app has no password: that one was
// removed and what that changes, or that there was none.
func printPasswordRemoved(out io.Writer, a appResource, had bool) error {
	var b strings.Builder
	if had {
		fmt.Fprintf(&b, "Removed the password of %s. %s\n", a.Slug, passwordGone(a))
	} else {
		fmt.Fprintf(&b, "%s has no password, so there was none to remove.\n", a.Slug)
	}
	b.WriteString(generalAccess(a, nil))
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the app's password: %w", err)
	}
	return nil
}
