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

	"github.com/latere-ai/latere-cli/internal/drive"
)

// newDriveCmd groups the Drive file-plane verbs (specs/003-drive-subcommand.md):
// eight orthogonal commands over https://drive.latere.ai/api/v1. Paths are
// namespace-rooted exactly as in the API (files/…, memory/…, repos/…,
// workspaces/…); variations are flags, not subcommand groups.
func newDriveCmd() *cobra.Command {
	var (
		driveURL string
		authURL  string
		token    string
		owner    string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "drive",
		Short: "Store, fetch, and share files on Latere Drive.",
		Long: `Work with files on Latere Drive (https://drive.latere.ai).

Paths are namespace-rooted as in the Drive API: files/…, memory/…,
repos/…, workspaces/…. Commands operate in your personal space by
default; --owner org (or an explicit u-<uuid>/o-<uuid>) selects
another space.

Uses the login saved by 'latere login'. Repo workspaces are served
over git — plain 'git clone https://drive.latere.ai/git/me/<repo>.git'
already works after login; see 'latere git-credential'.`,
		Example: `  latere drive ls
  latere drive put report.pdf files/reports/q2.pdf
  latere drive get files/reports/q2.pdf -o q2.pdf
  latere drive rm files/reports/q2.pdf
  latere drive restore files/reports/q2.pdf
  latere drive share files/reports/ --link`,
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&driveURL, "drive-url", "", "Drive base URL (default $DRIVE_API_URL or https://drive.latere.ai)")
	pf.StringVar(&authURL, "auth-url", "", "auth base URL for token refresh (default https://auth.latere.ai)")
	pf.StringVar(&token, "token", "", "present this bearer instead of the saved login (default $LATERE_DRIVE_TOKEN)")
	pf.StringVar(&owner, "owner", "me", "space to operate in: me, org, u-<uuid>, o-<uuid>")
	pf.BoolVar(&jsonOut, "json", false, "machine-readable JSON output on stdout")

	opts := &driveOpts{driveURL: &driveURL, authURL: &authURL, token: &token, owner: &owner, jsonOut: &jsonOut}
	cmd.AddCommand(newDriveLsCmd(opts))
	cmd.AddCommand(newDriveGetCmd(opts))
	cmd.AddCommand(newDrivePutCmd(opts))
	cmd.AddCommand(newDriveMvCmd(opts))
	cmd.AddCommand(newDriveRmCmd(opts))
	cmd.AddCommand(newDriveRestoreCmd(opts))
	cmd.AddCommand(newDriveHistoryCmd(opts))
	cmd.AddCommand(newDriveShareCmd(opts))
	cmd.AddCommand(newDriveSharesCmd(opts))
	cmd.AddCommand(newDriveUnshareCmd(opts))
	return cmd
}

// driveOpts carries the parent's persistent flags into the child factories.
type driveOpts struct {
	driveURL, authURL, token, owner *string
	jsonOut                         *bool
}

// client resolves the base URL and bearer for one command run.
func (o *driveOpts) client(ctx context.Context) (*drive.Client, error) {
	bearer, err := driveBearer(ctx, *o.token, *o.authURL)
	if err != nil {
		return nil, err
	}
	return drive.New(drive.ResolveURL(*o.driveURL), bearer), nil
}

// driveBearer resolves the bearer presented to Drive: an explicit --token,
// then $LATERE_DRIVE_TOKEN, then the saved login via the same refreshed
// auth-identity path the git credential helper uses.
func driveBearer(ctx context.Context, tokenFlag, authURL string) (string, error) {
	if t := strings.TrimSpace(tokenFlag); t != "" {
		return t, nil
	}
	if t := strings.TrimSpace(os.Getenv("LATERE_DRIVE_TOKEN")); t != "" {
		return t, nil
	}
	if tok, ok := driveCredentialToken(ctx, authURL); ok {
		return tok, nil
	}
	return "", errors.New("not signed in; run `latere login`")
}

// printDriveJSON emits one machine-readable value to stdout.
func printDriveJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---- ls ----

func newDriveLsCmd(o *driveOpts) *cobra.Command {
	var long, trashed bool
	cmd := &cobra.Command{
		Use:   "ls [prefix]",
		Short: "List files under a prefix (default files/).",
		Example: `  latere drive ls
  latere drive ls files/reports/ --long
  latere drive ls --trashed`,
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
			var entries []drive.FileEntry
			for cursor := ""; ; {
				page, err := c.List(cmd.Context(), *o.owner, prefix, cursor, 1000)
				if err != nil {
					return err
				}
				entries = append(entries, page.Entries...)
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if *o.jsonOut {
				return printDriveJSON(cmd.OutOrStdout(), entries)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, e := range entries {
				if long {
					fprintf(w, "%d\t%s\t%s\t%s\n", e.Size, e.Modified, e.Checksum, e.Path)
				} else {
					fprintln(w, e.Path)
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "show size, modified time, and checksum")
	cmd.Flags().BoolVar(&trashed, "trashed", false, "list trashed files instead of live ones")
	return cmd
}

func runLsTrashed(cmd *cobra.Command, c *drive.Client, o *driveOpts) error {
	var entries []drive.TrashEntry
	for cursor := ""; ; {
		page, err := c.TrashList(cmd.Context(), *o.owner, cursor, 1000)
		if err != nil {
			return err
		}
		entries = append(entries, page.Entries...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if *o.jsonOut {
		return printDriveJSON(cmd.OutOrStdout(), entries)
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	for _, e := range entries {
		fprintf(w, "%d\t%s\t%s\n", e.Size, e.DeletedAt, e.Path)
	}
	return w.Flush()
}

// driveVersionArgs keeps an explicitly requested revision from falling back
// to the current file (or whole-file deletion) when the revision is invalid.
func driveVersionArgs(version *int) cobra.PositionalArgs {
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

func newDriveGetCmd(o *driveOpts) *cobra.Command {
	var out string
	var version int
	cmd := &cobra.Command{
		Use:   "get <path>",
		Short: "Download one file.",
		Example: `  latere drive get files/reports/q2.pdf
  latere drive get files/notes.md -o -
  latere drive get files/notes.md --version 3 -o notes-v3.md`,
		Args: driveVersionArgs(&version),
		RunE: func(cmd *cobra.Command, args []string) error {
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

func newDrivePutCmd(o *driveOpts) *cobra.Command {
	var contentType, ifMatch string
	var createOnly bool
	cmd := &cobra.Command{
		Use:   "put <src> [path]",
		Short: "Upload one file (default destination files/<basename>).",
		Long: `Upload a local file. Files up to 16 MiB stream in a single request;
larger files go through Drive's multipart plane automatically (up to
16 GiB). '-' reads stdin (single-request; up to 100 MB).

Writes under memory/ require a compare-and-swap flag: --if-match with
the current checksum to overwrite, or --create-only for new files.`,
		Example: `  latere drive put report.pdf
  latere drive put report.pdf files/reports/q2.pdf
  latere drive put notes.md memory/notes.md --create-only
  cat data.csv | latere drive put - files/data.csv`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			dest := ""
			if len(args) == 2 {
				dest = args[1]
			}
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			opts := drive.PutOptions{ContentType: contentType, IfMatch: ifMatch, CreateOnly: createOnly}
			res, err := drivePut(cmd, c, *o.owner, src, dest, opts)
			if err != nil {
				return decorateCASError(err)
			}
			if *o.jsonOut {
				return printDriveJSON(cmd.OutOrStdout(), res)
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

// drivePut picks the upload strategy: stdin and small files stream a single
// PUT; anything past the fixed 16 MiB part size uses multipart.
func drivePut(cmd *cobra.Command, c *drive.Client, owner, src, dest string, opts drive.PutOptions) (*drive.FileWriteResult, error) {
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
	if st.Size() > drive.PartSize {
		return c.MultipartUpload(cmd.Context(), owner, dest, f, st.Size(), opts)
	}
	return c.Put(cmd.Context(), owner, dest, f, st.Size(), opts)
}

// decorateCASError turns Drive's CAS statuses into actionable guidance.
func decorateCASError(err error) error {
	var derr *drive.Error
	if !errors.As(err, &derr) {
		return err
	}
	switch derr.Status {
	case 412:
		return fmt.Errorf("%w\nThe file changed since you read it (or already exists). Get the current checksum with `latere drive ls --long`, then retry with --if-match", err)
	case 428:
		return fmt.Errorf("%w\nmemory/ writes need CAS: pass --if-match <checksum> to overwrite or --create-only for a new file", err)
	}
	return err
}

// ---- mv ----

func newDriveMvCmd(o *driveOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mv <src> <dst>",
		Short:   "Move or rename a file within a space.",
		Example: `  latere drive mv files/draft.md files/reports/final.md`,
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
				return printDriveJSON(cmd.OutOrStdout(), res)
			}
			fprintf(cmd.ErrOrStderr(), "Moved %s to %s\n", args[0], res.Path)
			return nil
		},
	}
	return cmd
}

// ---- rm ----

func newDriveRmCmd(o *driveOpts) *cobra.Command {
	var permanent bool
	var version int
	cmd := &cobra.Command{
		Use:   "rm <path>",
		Short: "Trash a file (--permanent to hard-delete, --version N to prune one version).",
		Example: `  latere drive rm files/old.txt
  latere drive rm files/old.txt --permanent
  latere drive rm files/notes.md --version 2`,
		Args: driveVersionArgs(&version),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			err = c.Delete(cmd.Context(), *o.owner, args[0], permanent, version)
			// A --permanent rm of an already-trashed file 404s on the
			// files route; purge it from the trash instead.
			var derr *drive.Error
			if permanent && version == 0 && errors.As(err, &derr) && derr.Status == 404 {
				if n, perr := c.TrashPurge(cmd.Context(), *o.owner, args[0]); perr == nil && n > 0 {
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
				fprintf(cmd.ErrOrStderr(), "Trashed %s (restore with `latere drive restore %s`)\n", args[0], args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&permanent, "permanent", false, "hard-delete, skipping the trash (also purges a trashed file)")
	cmd.Flags().IntVar(&version, "version", 0, "prune this single version; the file stays")
	return cmd
}

// ---- restore ----

func newDriveRestoreCmd(o *driveOpts) *cobra.Command {
	var version int
	cmd := &cobra.Command{
		Use:   "restore <path>",
		Short: "Restore a file from the trash, or to a prior version with --version N.",
		Example: `  latere drive restore files/old.txt
  latere drive restore files/notes.md --version 2`,
		Args: driveVersionArgs(&version),
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
					return printDriveJSON(cmd.OutOrStdout(), res)
				}
				fprintf(cmd.ErrOrStderr(), "Restored %s to version %d\n", res.Path, res.RestoredVersion)
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

func newDriveHistoryCmd(o *driveOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "history <path>",
		Short:   "List a file's version history.",
		Example: `  latere drive history files/notes.md`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			var entries []drive.FileVersionEntry
			for cursor := ""; ; {
				page, err := c.Versions(cmd.Context(), *o.owner, args[0], cursor, 1000)
				if err != nil {
					return err
				}
				entries = append(entries, page.Entries...)
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if *o.jsonOut {
				return printDriveJSON(cmd.OutOrStdout(), entries)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, v := range entries {
				by := v.CreatedByDisplay
				if by == "" {
					by = v.CreatedBy
				}
				fprintf(w, "v%d\t%d\t%s\t%s\t%s\n", v.VersionNo, v.Size, v.SupersededAt, by, v.Checksum)
			}
			return w.Flush()
		},
	}
	return cmd
}

// ---- share / shares / unshare ----

func newDriveShareCmd(o *driveOpts) *cobra.Command {
	var to, permission, expires string
	var link, public bool
	cmd := &cobra.Command{
		Use:   "share <path-prefix>",
		Short: "Grant access to a path prefix (a person via --to, or a link via --link).",
		Long: `Share files under a path prefix.

--link mints a tokenized viewer URL (read-only). --to grants a person:
an email address, or a principal id. --permission read|write|manage
applies to person grants; links are always read-only.`,
		Example: `  latere drive share files/reports/ --link
  latere drive share files/reports/ --to teammate@example.com
  latere drive share files/data/ --to u-1234… --permission write`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := drive.CreateShareRequest{
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
				return printDriveJSON(cmd.OutOrStdout(), res)
			}
			state := "created"
			if res.Existing {
				state = "already exists"
			}
			fprintf(cmd.ErrOrStderr(), "Share %s (%s, %s, id %s)\n", state, res.Status, res.Permission, res.ID)
			if res.URL != "" {
				fprintln(cmd.OutOrStdout(), drive.ResolveURL(*o.driveURL)+res.URL)
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

func newDriveSharesCmd(o *driveOpts) *cobra.Command {
	var inbox bool
	cmd := &cobra.Command{
		Use:   "shares",
		Short: "List shares you created (--inbox: shares granted to you).",
		Example: `  latere drive shares
  latere drive shares --inbox`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			var entries []drive.Share
			for cursor := ""; ; {
				page, err := c.Shares(cmd.Context(), inbox, cursor, 1000)
				if err != nil {
					return err
				}
				entries = append(entries, page.Entries...)
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if *o.jsonOut {
				return printDriveJSON(cmd.OutOrStdout(), entries)
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

func newDriveUnshareCmd(o *driveOpts) *cobra.Command {
	return &cobra.Command{
		Use:     "unshare <share-id>",
		Short:   "Revoke a share.",
		Example: `  latere drive unshare 3fa85f64-5717-4562-b3fc-2c963f66afa6`,
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
