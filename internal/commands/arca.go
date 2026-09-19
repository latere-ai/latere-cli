// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/arca"
)

// arcaName is the group's name. root.go matches the update check against
// it, so what the group is called and what stays quiet cannot drift apart.
const arcaName = "arca"

// arcaAudience is the aud claim Arca enforces on every bearer it accepts.
// It is the audience whatever origin ARCA_API_URL names: the URL selects
// the deployment, the audience is what the issuer stamps. This file is the
// one place the CLI mints for it.
const arcaAudience = "arca"

// newArcaCmd groups the storage verbs (specs/003-arca-subcommand.md):
// ten orthogonal commands over the origin's /v1. Paths are plane-rooted
// exactly as in the API (files/…, workspaces/…); variations are flags, not
// subcommand groups.
func newArcaCmd() *cobra.Command {
	var (
		apiURL  string
		authURL string
		token   string
		owner   string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   arcaName,
		Short: "Store, fetch, and share your files.",
		Long: "Work with your files on Latere's storage service, at " + arca.DefaultBaseURL + `.

Paths are plane-rooted as in the API: files/…, workspaces/…. Commands
operate in your personal space by default; --owner takes the subject of
another space, which every listing prints in full so you can send it
back.

Uses the login saved by 'latere login'.`,
		Example: `  latere arca ls
  latere arca put report.pdf files/reports/q2.pdf
  latere arca get files/reports/q2.pdf -o q2.pdf
  latere arca rm files/reports/q2.pdf
  latere arca restore files/reports/q2.pdf
  latere arca share files/reports/ --link`,
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&apiURL, "api-url", "", "API origin (default $ARCA_API_URL or "+arca.DefaultBaseURL+")")
	pf.StringVar(&authURL, "auth-url", "", "auth base URL for token refresh (default https://auth.latere.ai)")
	pf.StringVar(&token, "token", "", "present this bearer instead of the saved login (default $LATERE_ARCA_TOKEN)")
	pf.StringVar(&owner, "owner", "me", "space to operate in: me, or a subject a listing showed you")
	pf.BoolVar(&jsonOut, "json", false, "machine-readable JSON output on stdout")

	opts := &arcaOpts{apiURL: &apiURL, authURL: &authURL, token: &token, owner: &owner, jsonOut: &jsonOut}
	cmd.AddCommand(newArcaLsCmd(opts))
	cmd.AddCommand(newArcaGetCmd(opts))
	cmd.AddCommand(newArcaPutCmd(opts))
	cmd.AddCommand(newArcaMvCmd(opts))
	cmd.AddCommand(newArcaRmCmd(opts))
	cmd.AddCommand(newArcaRestoreCmd(opts))
	cmd.AddCommand(newArcaHistoryCmd(opts))
	cmd.AddCommand(newArcaShareCmd(opts))
	cmd.AddCommand(newArcaSharesCmd(opts))
	cmd.AddCommand(newArcaUnshareCmd(opts))
	return cmd
}

// arcaOpts carries the parent's persistent flags into the child factories.
type arcaOpts struct {
	apiURL, authURL, token, owner *string
	jsonOut                       *bool
}

// client resolves the origin and bearer for one command run.
func (o *arcaOpts) client(ctx context.Context) (*arca.Client, error) {
	if err := checkOwner(*o.owner); err != nil {
		return nil, err
	}
	bearer, err := arcaBearer(ctx, *o.token, *o.authURL)
	if err != nil {
		return nil, err
	}
	return arca.New(arca.ResolveURL(*o.apiURL), bearer), nil
}

// checkOwner refuses the three spellings the predecessor addressed a space
// with. A space is a subject now, and `org`, `u-<uuid>` and `o-<uuid>` are
// all valid subject strings that name nothing, so without this they would
// list an empty space rather than say what is wrong.
func checkOwner(owner string) error {
	switch {
	case owner == "org":
		return errors.New("`org` is no longer a space; pass the organization's subject, which every listing prints in full")
	case strings.HasPrefix(owner, "u-"), strings.HasPrefix(owner, "o-"):
		return fmt.Errorf("%q is no longer a space; pass the subject, which every listing prints in full", owner)
	}
	return nil
}

// arcaCredentialToken is the bearer presented to Arca: a token minted for
// arcaAudience alone from the saved login, the same path the git credential
// helper uses for its own product.
func arcaCredentialToken(ctx context.Context, authURL string) (string, error) {
	bearer, _, err := api.ActorToken(ctx, api.ResolveAuthURL(arca.ResolveURL(""), authURL), arcaAudience)
	if err != nil {
		return "", fmt.Errorf("cannot authenticate to the storage service: %w", err)
	}
	return bearer, nil
}

// arcaBearer resolves the bearer: an explicit --token, then
// $LATERE_ARCA_TOKEN, then a minted actor token. A missing login reads as
// "not signed in"; a refresh or mint failure is reported as itself, since
// signing in again is not always the fix.
func arcaBearer(ctx context.Context, tokenFlag, authURL string) (string, error) {
	if t := strings.TrimSpace(tokenFlag); t != "" {
		return t, nil
	}
	if t := strings.TrimSpace(os.Getenv("LATERE_ARCA_TOKEN")); t != "" {
		return t, nil
	}
	return arcaCredentialToken(ctx, authURL)
}

// printArcaJSON emits one machine-readable value to stdout.
func printArcaJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func trackArcaCursor(seen map[string]bool, cursor string) error {
	if seen[cursor] {
		return errors.New("the listing returned a cursor it already gave")
	}
	seen[cursor] = true
	return nil
}

// ---- ls ----

func newArcaLsCmd(o *arcaOpts) *cobra.Command {
	var long, trashed bool
	cmd := &cobra.Command{
		Use:   "ls [prefix]",
		Short: "List files under a prefix (default files/).",
		Example: `  latere arca ls
  latere arca ls files/reports/ --long
  latere arca ls --trashed`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := "files/"
			if len(args) == 1 {
				prefix = args[0]
			}
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			if trashed {
				return runLsTrashed(cmd, c, o)
			}
			var entries []arca.Object
			seen := make(map[string]bool)
			for cursor := ""; ; {
				page, err := c.List(cmd.Context(), *o.owner, prefix, cursor, 1000)
				if err != nil {
					return err
				}
				entries = append(entries, page.Entries...)
				if page.NextCursor == "" {
					break
				}
				if err := trackArcaCursor(seen, page.NextCursor); err != nil {
					return err
				}
				cursor = page.NextCursor
			}
			if *o.jsonOut {
				return printArcaJSON(cmd.OutOrStdout(), entries)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, e := range entries {
				if long {
					if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", e.Size, e.Modified, e.Checksum, e.Path); err != nil {
						return fmt.Errorf("write Arca file list: %w", err)
					}
				} else {
					if _, err := fmt.Fprintln(w, e.Path); err != nil {
						return fmt.Errorf("write Arca file list: %w", err)
					}
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "show size, modified time, and checksum")
	cmd.Flags().BoolVar(&trashed, "trashed", false, "list trashed files instead of live ones")
	return cmd
}

func runLsTrashed(cmd *cobra.Command, c *arca.Client, o *arcaOpts) error {
	var entries []arca.Trashed
	seen := make(map[string]bool)
	for cursor := ""; ; {
		page, err := c.TrashList(cmd.Context(), *o.owner, cursor, 1000)
		if err != nil {
			return err
		}
		entries = append(entries, page.Entries...)
		if page.NextCursor == "" {
			break
		}
		if err := trackArcaCursor(seen, page.NextCursor); err != nil {
			return err
		}
		cursor = page.NextCursor
	}
	if *o.jsonOut {
		return printArcaJSON(cmd.OutOrStdout(), entries)
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	for _, e := range entries {
		fprintf(w, "%d\t%s\t%s\n", e.Size, e.DeletedAt, e.Path)
	}
	return w.Flush()
}

// arcaVersionArgs keeps an explicitly requested revision from falling back
// to the current file (or whole-file deletion) when the revision is invalid.
func arcaVersionArgs(version *int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		if cmd.Flags().Changed("version") && *version <= 0 {
			return errors.New("--version must be a positive integer")
		}
		return nil
	}
}

// ---- get ----

func newArcaGetCmd(o *arcaOpts) *cobra.Command {
	var out string
	var version int
	cmd := &cobra.Command{
		Use:   "get <path>",
		Short: "Download one file.",
		Example: `  latere arca get files/reports/q2.pdf
  latere arca get files/notes.md -o -
  latere arca get files/notes.md --version 3 -o notes-v3.md`,
		Args: arcaVersionArgs(&version),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("output") && out == "" {
				return errors.New("--output cannot be empty; use '-' for stdout")
			}
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			body, _, err := c.Download(cmd.Context(), *o.owner, args[0], version)
			if err != nil {
				return err
			}
			defer func() { _ = body.Close() }()

			if out == "-" {
				_, err = io.Copy(cmd.OutOrStdout(), body)
				return err
			}
			dest := out
			if dest == "" {
				dest = path.Base(args[0])
			}
			if err := saveDownload(dest, body); err != nil {
				return err
			}
			fprintf(cmd.ErrOrStderr(), "Downloaded %s to %s\n", args[0], dest)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "destination file (default: basename of path; '-' for stdout)")
	cmd.Flags().IntVar(&version, "version", 0, "download this version instead of the current one")
	return cmd
}

// ---- put ----

func newArcaPutCmd(o *arcaOpts) *cobra.Command {
	var contentType, ifMatch string
	var createOnly bool
	cmd := &cobra.Command{
		Use:   "put <src> [path]",
		Short: "Upload one file (default destination files/<basename>).",
		Long: `Upload a local file. Files up to 16 MiB stream in a single request;
larger files go through Arca's multipart plane automatically (up to
16 GiB). '-' reads stdin (single-request; up to 100 MB).

Writes under memory/ require a compare-and-swap flag: --if-match with
the current checksum to overwrite, or --create-only for new files.`,
		Example: `  latere arca put report.pdf
  latere arca put report.pdf files/reports/q2.pdf
  latere arca put notes.md memory/notes.md --create-only
  cat data.csv | latere arca put - files/data.csv`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			dest := ""
			if len(args) == 2 {
				dest = args[1]
				if strings.TrimSpace(dest) == "" {
					return errors.New("destination path cannot be empty")
				}
			}
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			opts := arca.PutOptions{ContentType: contentType, IfMatch: ifMatch, CreateOnly: createOnly}
			res, err := arcaPut(cmd, c, *o.owner, src, dest, opts)
			if err != nil {
				return decorateCASError(err)
			}
			if *o.jsonOut {
				return printArcaJSON(cmd.OutOrStdout(), res)
			}
			fprintf(cmd.ErrOrStderr(), "Uploaded %s (%d bytes, checksum %s)\n", res.Path, res.Size, res.Checksum)
			return nil
		},
	}
	cmd.Flags().StringVar(&contentType, "content-type", "", "stored content type (default detected by the server)")
	cmd.Flags().StringVar(&ifMatch, "if-match", "", "overwrite only if the current checksum matches (CAS)")
	cmd.Flags().BoolVar(&createOnly, "create-only", false, "fail if the file already exists (If-None-Match: *)")
	return cmd
}

// arcaPut picks the upload strategy: stdin and small files stream a single
// PUT; anything past the fixed 16 MiB part size uses multipart.
func arcaPut(cmd *cobra.Command, c *arca.Client, owner, src, dest string, opts arca.PutOptions) (*arca.Object, error) {
	if src == "-" {
		if dest == "" {
			return nil, errors.New("uploading from stdin requires an explicit destination path")
		}
		// A single PUT needs Content-Length up front, so stdin is
		// buffered; the server caps single uploads at 100 MB anyway.
		b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), (100<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(b) > 100<<20 {
			return nil, errors.New("stdin exceeds the 100 MB single-upload cap; write it to a file first")
		}
		return c.Put(cmd.Context(), owner, dest, strings.NewReader(string(b)), int64(len(b)), opts)
	}

	// Reject non-regular sources before opening: FIFOs can block and devices
	// often report size zero. Stat follows symlinks to ordinary files.
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("upload source %q is not a regular file; use '-' to read stdin", src)
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("upload source %q is not a regular file", src)
	}
	if dest == "" {
		dest = "files/" + path.Base(src)
	}
	if st.Size() > arca.PartSize {
		return c.MultipartUpload(cmd.Context(), owner, dest, f, st.Size(), opts)
	}
	return c.Put(cmd.Context(), owner, dest, f, st.Size(), opts)
}

// decorateCASError adds what to do next to a refused conditional write.
// No route demands a precondition, so this can only follow one the caller
// asked for: --if-match names a checksum the object no longer has, or
// --create-only found something already there.
func decorateCASError(err error) error {
	var derr *arca.Error
	if !errors.As(err, &derr) {
		return err
	}
	if derr.Code == "precondition_failed" {
		return fmt.Errorf("%w\nThe file changed since you read it, or it already exists. Read the current checksum with `latere arca ls --long`, then retry with --if-match", err)
	}
	return err
}

// ---- mv ----

func newArcaMvCmd(o *arcaOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mv <src> <dst>",
		Short:   "Move or rename a file within a space.",
		Example: `  latere arca mv files/draft.md files/reports/final.md`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			res, err := c.Move(cmd.Context(), *o.owner, args[0], args[1])
			if err != nil {
				return err
			}
			if *o.jsonOut {
				return printArcaJSON(cmd.OutOrStdout(), res)
			}
			fprintf(cmd.ErrOrStderr(), "Moved %s to %s\n", args[0], res.Path)
			return nil
		},
	}
	return cmd
}

// ---- rm ----

func newArcaRmCmd(o *arcaOpts) *cobra.Command {
	var permanent bool
	var version int
	cmd := &cobra.Command{
		Use:   "rm <path>",
		Short: "Trash a file (--permanent to hard-delete, --version N to prune one version).",
		Example: `  latere arca rm files/old.txt
  latere arca rm files/old.txt --permanent
  latere arca rm files/notes.md --version 2`,
		Args: arcaVersionArgs(&version),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			err = c.Delete(cmd.Context(), *o.owner, args[0], permanent, version)
			// A --permanent rm of an already-trashed file 404s on the
			// files route; purge it from the trash instead.
			var derr *arca.Error
			if permanent && version == 0 && errors.As(err, &derr) && derr.Status == 404 {
				n, perr := c.TrashPurge(cmd.Context(), *o.owner, args[0])
				if perr != nil {
					return fmt.Errorf("purge trashed file %q: %w", args[0], perr)
				}
				if n > 0 {
					err = nil
				}
			}
			if err != nil {
				return err
			}
			switch {
			case version > 0:
				fprintf(cmd.ErrOrStderr(), "Pruned version %d of %s\n", version, args[0])
			case permanent:
				fprintf(cmd.ErrOrStderr(), "Permanently deleted %s\n", args[0])
			default:
				fprintf(cmd.ErrOrStderr(), "Trashed %s (restore with `latere arca restore %s`)\n", args[0], args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&permanent, "permanent", false, "hard-delete, skipping the trash (also purges a trashed file)")
	cmd.Flags().IntVar(&version, "version", 0, "prune this single version; the file stays")
	return cmd
}

// ---- restore ----

func newArcaRestoreCmd(o *arcaOpts) *cobra.Command {
	var version int
	cmd := &cobra.Command{
		Use:   "restore <path>",
		Short: "Restore a file from the trash, or to a prior version with --version N.",
		Example: `  latere arca restore files/old.txt
  latere arca restore files/notes.md --version 2`,
		Args: arcaVersionArgs(&version),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			if version > 0 {
				res, err := c.RestoreVersion(cmd.Context(), *o.owner, args[0], version)
				if err != nil {
					return err
				}
				if *o.jsonOut {
					return printArcaJSON(cmd.OutOrStdout(), res)
				}
				fprintf(cmd.ErrOrStderr(), "Restored %s to version %d (checksum %s)\n", res.Path, version, res.Checksum)
				return nil
			}
			if err := c.TrashRestore(cmd.Context(), *o.owner, args[0]); err != nil {
				return err
			}
			fprintf(cmd.ErrOrStderr(), "Restored %s from trash\n", args[0])
			return nil
		},
	}
	cmd.Flags().IntVar(&version, "version", 0, "restore this version in place instead of restoring from trash")
	return cmd
}

// ---- history ----

func newArcaHistoryCmd(o *arcaOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "history <path>",
		Short:   "List a file's version history.",
		Example: `  latere arca history files/notes.md`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			var entries []arca.Version
			seen := make(map[string]bool)
			for cursor := ""; ; {
				page, err := c.Versions(cmd.Context(), *o.owner, args[0], cursor, 1000)
				if err != nil {
					return err
				}
				entries = append(entries, page.Entries...)
				if page.NextCursor == "" {
					break
				}
				if err := trackArcaCursor(seen, page.NextCursor); err != nil {
					return err
				}
				cursor = page.NextCursor
			}
			if *o.jsonOut {
				return printArcaJSON(cmd.OutOrStdout(), entries)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, v := range entries {
				fprintf(w, "v%d\t%d\t%s\t%s\t%s\n", v.VersionNo, v.Size, v.SupersededAt, v.CreatedBy, v.Checksum)
			}
			return w.Flush()
		},
	}
	return cmd
}

// ---- share / shares / unshare ----

func newArcaShareCmd(o *arcaOpts) *cobra.Command {
	var to, permission, expires string
	var link, public bool
	cmd := &cobra.Command{
		Use:   "share <path-prefix>",
		Short: "Grant access to a path prefix (a person via --to, or a link via --link).",
		Long: `Share files under a path prefix.

--link mints a tokenized viewer URL (read-only). --to grants a person:
an email address, or a principal id. --permission read|write|manage
applies to person grants; links are always read-only.`,
		Example: `  latere arca share files/reports/ --link
  latere arca share files/reports/ --to teammate@example.com
  latere arca share files/data/ --to u-1234… --permission write`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := arca.CreateShareRequest{
				Owner:      *o.owner,
				PathPrefix: args[0],
				Permission: permission,
				ExpiresAt:  expires,
			}
			switch {
			case link && to == "" && !public:
				req.GranteeType = "link"
			case public && to == "" && !link:
				req.GranteeType = "public"
			case to != "" && !link && !public:
				if strings.Contains(to, "@") {
					req.GranteeType = "email"
					req.GranteeEmail = to
				} else {
					req.GranteeType = "principal"
					req.GranteeID = to
				}
			default:
				return errors.New("pass exactly one of --to <email|principal-id>, --link, or --public")
			}
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			res, err := c.CreateShare(cmd.Context(), req)
			if err != nil {
				return err
			}
			if *o.jsonOut {
				return printArcaJSON(cmd.OutOrStdout(), res)
			}
			state := "created"
			if res.Existing {
				state = "already exists"
			}
			fprintf(cmd.ErrOrStderr(), "Share %s (%s, %s, id %s)\n", state, res.Status, res.Permission, res.ID)
			if res.URL != "" {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), arca.ResolveURL(*o.apiURL)+res.URL); err != nil {
					return fmt.Errorf("write share URL for share %s: %w", res.ID, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "grantee: an email address or principal id")
	cmd.Flags().BoolVar(&link, "link", false, "mint a read-only viewer link")
	cmd.Flags().BoolVar(&public, "public", false, "make the prefix publicly readable")
	cmd.Flags().StringVar(&permission, "permission", "read", "read, write, or manage (person grants only)")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry as RFC3339 (e.g. 2026-12-31T00:00:00Z)")
	return cmd
}

func newArcaSharesCmd(o *arcaOpts) *cobra.Command {
	var inbox bool
	cmd := &cobra.Command{
		Use:   "shares",
		Short: "List shares you created (--inbox: shares granted to you).",
		Example: `  latere arca shares
  latere arca shares --inbox`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			var entries []arca.Share
			seen := make(map[string]bool)
			for cursor := ""; ; {
				page, err := c.Shares(cmd.Context(), inbox, cursor, 1000)
				if err != nil {
					return err
				}
				entries = append(entries, page.Entries...)
				if page.NextCursor == "" {
					break
				}
				if err := trackArcaCursor(seen, page.NextCursor); err != nil {
					return err
				}
				cursor = page.NextCursor
			}
			if *o.jsonOut {
				return printArcaJSON(cmd.OutOrStdout(), entries)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, s := range entries {
				who := s.GranteeDisplay
				if who == "" {
					who = s.GranteeEmail
				}
				if who == "" {
					who = s.GranteeID
				}
				if who == "" {
					who = s.GranteeType
				}
				fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Status, s.Permission, who, s.PathPrefix)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&inbox, "inbox", false, "list shares granted to you instead of by you")
	return cmd
}

func newArcaUnshareCmd(o *arcaOpts) *cobra.Command {
	return &cobra.Command{
		Use:     "unshare <share-id>",
		Short:   "Revoke a share.",
		Example: `  latere arca unshare 3fa85f64-5717-4562-b3fc-2c963f66afa6`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			if err := c.RevokeShare(cmd.Context(), args[0]); err != nil {
				return err
			}
			fprintf(cmd.ErrOrStderr(), "Revoked share %s\n", args[0])
			return nil
		},
	}
}
