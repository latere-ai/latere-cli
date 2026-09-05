// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
// no token in the URL after `latere login`.
func newGitCredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git-credential",
		Short: "Git credential helper for Drive (drive.latere.ai).",
		Long: `Authenticate git against Drive (drive.latere.ai) with the token saved
by 'latere login'.

git invokes this helper as 'latere git-credential get|store|erase',
writing an attribute block (protocol, host, ...) to stdin. 'get' answers
only for the Drive host and emits the saved login as username/password
lines, refreshing the token first when it has expired. 'store' and
'erase' are no-ops: the token lives in ~/.config/latere, managed by
'latere login' and 'latere logout', never in git's own store.

Run 'latere git-credential setup' once to wire the helper into your
global git config, scoped to drive.latere.ai only.`,
		Example: `  latere login
  latere git-credential setup
  git clone https://drive.latere.ai/git/me/<repo>.git`,
	}
	cmd.AddCommand(newGitCredentialGetCmd())
	cmd.AddCommand(newGitCredentialNoopCmd("store"))
	cmd.AddCommand(newGitCredentialNoopCmd("erase"))
	cmd.AddCommand(newGitCredentialSetupCmd())
	return cmd
}

// newGitCredentialSetupCmd wires the helper into the user's global git
// config, scoped to the Drive host only. Two entries are written: an empty
// helper first, which makes git discard credential helpers inherited from
// broader config scopes (e.g. osxkeychain from the system gitconfig) for
// this host — so no other helper caches or serves a stale Drive token —
// then the real helper.
func newGitCredentialSetupCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure git to use this helper for Drive (undo with --remove).",
		Long: `Write the global git config entries that route Drive credentials
through this helper:

    credential.https://<drive-host>.helper =                        (reset)
    credential.https://<drive-host>.helper = !latere git-credential

The empty first entry clears helpers inherited from wider git config
scopes for the Drive host, so only this helper answers there. Helpers
for every other host are untouched. Re-running setup is idempotent;
--remove deletes both entries.`,
		Example: `  latere git-credential setup
  latere git-credential setup --remove`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			key := driveGitHelperKey()
			errw := cmd.ErrOrStderr()
			if remove {
				if err := gitConfigUnsetAll(cmd.Context(), key); err != nil {
					return err
				}
				fprintf(errw, "Removed %s from the global git config.\n", key)
				return nil
			}
			if err := writeDriveGitHelperConfig(cmd.Context()); err != nil {
				return err
			}
			fprintf(errw, "Configured the global git config:\n")
			fprintf(errw, "  %s=                          (resets inherited helpers)\n", key)
			fprintf(errw, "  %s=!latere git-credential\n\n", key)
			fprintf(errw, "git clone https://%s/git/me/<repo>.git now authenticates\nwith the token from `latere login`.\n", driveHost())
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the Drive credential-helper entries from the global git config")
	return cmd
}

func driveGitHelperKey() string {
	return fmt.Sprintf("credential.https://%s.helper", driveHost())
}

// writeDriveGitHelperConfig writes the reset + helper entries for the Drive
// host into the global git config. --replace-all collapses any previous
// entries into the single empty reset entry, making re-runs idempotent;
// --add appends the real helper after it.
func writeDriveGitHelperConfig(ctx context.Context) error {
	key := driveGitHelperKey()
	if err := gitConfig(ctx, "--replace-all", key, ""); err != nil {
		return err
	}
	return gitConfig(ctx, "--add", key, "!latere git-credential")
}

// driveGitHelperConfigured reports whether the global git config already
// carries exactly the two entries setup writes (reset + helper).
func driveGitHelperConfigured(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "git", "config", "--global", "--get-all", driveGitHelperKey()).Output()
	if err != nil {
		return false
	}
	return string(out) == "\n!latere git-credential\n"
}

// configureDriveGitAfterLogin is the post-login hook `latere login`
// runs (unless --no-git). Swappable for tests.
var configureDriveGitAfterLogin = autoConfigureDriveGit

// autoConfigureDriveGit wires the Drive credential helper after a
// successful login. Best-effort by design — login must never fail over git
// config: no git binary on PATH is a silent skip, and a git config error
// degrades to one quiet warning pointing at the manual command. Skips the
// write when the entries are already in place.
func autoConfigureDriveGit(ctx context.Context, errw io.Writer) {
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	if !driveGitHelperConfigured(ctx) {
		if err := writeDriveGitHelperConfig(ctx); err != nil {
			fprintf(errw, "  warning: could not configure git for %s (%v); run `latere git-credential setup` manually\n", driveHost(), err)
			return
		}
	}
	fprintf(errw, "git is configured for %s (clone with git clone https://%s/git/<handle>/<repo>.git)\n", driveHost(), driveHost())
}

// gitConfig runs `git config --global <args>`. Tests point it at a scratch
// file via the GIT_CONFIG_GLOBAL environment variable, which git honors.
func gitConfig(ctx context.Context, args ...string) error {
	full := append([]string{"config", "--global"}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitConfigUnsetAll removes every value of key from the global git config.
// git exits 5 when the key is not set; removing nothing is success, so
// `setup --remove` stays idempotent.
func gitConfigUnsetAll(ctx context.Context, key string) error {
	out, err := exec.CommandContext(ctx, "git", "config", "--global", "--unset-all", key).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 5 {
			return nil
		}
		return fmt.Errorf("git config --global --unset-all %s: %w: %s", key, err, strings.TrimSpace(string(out)))
	}
	return nil
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
			if !isDriveCredentialRequest(cmd.InOrStdin()) {
				return nil
			}
			access, ok := driveCredentialToken(cmd.Context(), authURL)
			if !ok {
				return nil
			}
			// Drive's git endpoint reads the Basic password as the bearer
			// token; the username is ignored, `token` by convention.
			fprintf(cmd.OutOrStdout(), "username=token\npassword=%s\n\n", access)
			return nil
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "auth service base URL used for token refresh (default $AUTH_URL or https://auth.latere.ai)")
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
// JWTs). Falls back to token.json only when the auth file is absent, as it is
// after --token paste login. Existing auth failures must not change identity.
func driveCredentialToken(ctx context.Context, authURL string) (string, bool) {
	access, _, err := authIdentityToken(ctx, "", authURL)
	if err == nil {
		return access, access != ""
	}
	if !errors.Is(err, api.ErrNoToken) {
		return "", false
	}
	if tok, err := api.LoadToken(""); err == nil && tok.AccessToken != "" {
		return tok.AccessToken, true
	}
	return "", false
}

// parseCredentialAttrs reads git's credential-helper attribute block: one
// `key=value` per line, terminated by a blank line or EOF. Values may
// contain `=`; lines without one are ignored, matching git's tolerance.
// isDriveCredentialRequest reports whether git is asking for a credential
// this helper answers. Every miss reads the same -- an unreadable request as
// much as another host -- because a credential helper must never break
// `git fetch`: the command then prints nothing, exits 0, and git prompts.
func isDriveCredentialRequest(r io.Reader) bool {
	attrs, err := parseCredentialAttrs(r)
	if err != nil {
		return false
	}
	return driveCredentialRequest(attrs)
}

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
