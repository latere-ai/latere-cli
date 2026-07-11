package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

// defaultDriveHost is the public Drive deployment. DRIVE_HOST overrides it
// for dev deployments; the value is compared against git's `host` attribute
// verbatim, so it may carry a port (e.g. localhost:8080).
const defaultDriveHost = "drive.latere.ai"

func driveHost() string {
	if v := strings.TrimSpace(os.Getenv("DRIVE_HOST")); v != "" {
		return v
	}
	return defaultDriveHost
}

// newGitCredentialCmd is the git credential helper for Drive. git invokes it
// as `latere git-credential get|store|erase` with an attribute block on
// stdin, so `git clone https://drive.latere.ai/git/me/<repo>.git` works with
// no token in the URL after `latere auth login`.
func newGitCredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git-credential",
		Short: "Git credential helper for Drive (drive.latere.ai).",
		Long: `Authenticate git against Drive (drive.latere.ai) with the token saved
by 'latere auth login'.

git invokes this helper as 'latere git-credential get|store|erase',
writing an attribute block (protocol, host, ...) to stdin. 'get' answers
only for the Drive host and emits the saved login as username/password
lines, refreshing the token first when it has expired. 'store' and
'erase' are no-ops: the token lives in ~/.config/latere, managed by
'latere auth login' and 'latere auth logout', never in git's own store.

Run 'latere git-credential setup' once to wire the helper into your
global git config, scoped to drive.latere.ai only.`,
		Example: `  latere auth login
  latere git-credential setup
  git clone https://drive.latere.ai/git/me/<repo>.git`,
	}
	cmd.AddCommand(newGitCredentialGetCmd())
	cmd.AddCommand(newGitCredentialNoopCmd("store"))
	cmd.AddCommand(newGitCredentialNoopCmd("erase"))
	return cmd
}

// newGitCredentialGetCmd implements the `get` operation of the
// git-credential protocol. It is deliberately quiet: any miss (other host,
// not logged in, unreadable token) prints nothing and exits 0 so git falls
// back to prompting — a credential helper must never break `git fetch`.
func newGitCredentialGetCmd() *cobra.Command {
	var authURL string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Emit the saved Latere login for a Drive git request (called by git).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			attrs, err := parseCredentialAttrs(cmd.InOrStdin())
			if err != nil {
				return nil
			}
			if !driveCredentialRequest(attrs) {
				return nil
			}
			access, ok := driveCredentialToken(cmd.Context(), authURL)
			if !ok {
				return nil
			}
			// Drive's git endpoint reads the Basic password as the bearer
			// token; the username is ignored, `token` by convention.
			fmt.Fprintf(cmd.OutOrStdout(), "username=token\npassword=%s\n\n", access)
			return nil
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "auth service base URL used for token refresh (default https://auth.latere.ai)")
	return cmd
}

// newGitCredentialNoopCmd covers `store` and `erase`. git calls store after
// a successful fetch and erase after a rejected credential; both are no-ops
// because the CLI's own token store is the source of truth. The attribute
// block is still drained so git never sees a broken pipe.
func newGitCredentialNoopCmd(op string) *cobra.Command {
	return &cobra.Command{
		Use:   op,
		Short: fmt.Sprintf("No-op %s operation (tokens live in the CLI's own store).", op),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = parseCredentialAttrs(cmd.InOrStdin())
			return nil
		},
	}
}

// driveCredentialRequest reports whether the attribute block names the Drive
// deployment this CLI serves credentials for. Production requires https;
// a DRIVE_HOST override (dev deployments) may be plain http.
func driveCredentialRequest(attrs map[string]string) bool {
	if !strings.EqualFold(attrs["host"], driveHost()) {
		return false
	}
	if p := attrs["protocol"]; p != "" && p != "https" && os.Getenv("DRIVE_HOST") == "" {
		return false
	}
	return true
}

// driveCredentialToken resolves the bearer git presents to Drive: the
// retained auth root token, refreshed when expired via the same
// authIdentityToken path `latere lux` uses (Drive validates auth-issued
// JWTs). Falls back to token.json for --token paste logins. Returns false
// when no login is on file.
func driveCredentialToken(ctx context.Context, authURL string) (string, bool) {
	if access, _, err := authIdentityToken(ctx, "", authURL); err == nil && access != "" {
		return access, true
	}
	if tok, err := api.LoadToken(""); err == nil && tok.AccessToken != "" {
		return tok.AccessToken, true
	}
	return "", false
}

// parseCredentialAttrs reads git's credential-helper attribute block: one
// `key=value` per line, terminated by a blank line or EOF. Values may
// contain `=`; lines without one are ignored, matching git's tolerance.
func parseCredentialAttrs(r io.Reader) (map[string]string, error) {
	attrs := make(map[string]string)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			attrs[k] = v
		}
	}
	return attrs, sc.Err()
}
