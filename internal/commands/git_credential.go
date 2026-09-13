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

// defaultCodeHost is the public Latere Code deployment (Origo). CODE_HOST
// overrides it for dev deployments; the value is compared against git's
// `host` attribute verbatim, so it may carry a port (e.g. localhost:8081).
const defaultCodeHost = "code.latere.ai"

// gitTarget is one git host this helper answers for, and the audience auth
// stamps on the token it mints for that host. The host and the audience are
// separate fields because they are separate things: Origo enforces the
// literal "origo" (its internal/auth.AudienceOrigo), not its hostname, so
// neither follows from the other.
type gitTarget struct {
	// name is what a message calls this deployment.
	name string
	// host is the git hostname, after its environment override.
	host string
	// audience is the aud claim the service requires on the bearer git
	// presents. Always the production value: the host override selects
	// which git host the helper answers for, not what auth stamps.
	audience string
	// overridden reports whether the host came from an environment
	// override, which is what allows plain http for a dev deployment.
	overridden bool
	// username is what the helper puts in git's username line. Both hosts
	// read only the password as the bearer and ignore the username; each
	// value is the convention that host's own documentation uses.
	username string
	// cloneHint is the example printed after login.
	cloneHint string
}

// gitTargets is the table the helper, setup, and the post-login hook all
// read. Adding a git host is one row here.
func gitTargets() []gitTarget {
	code := gitTarget{
		name: "Latere Code", host: defaultCodeHost, audience: codeAudience,
		username:  "x-access-token",
		cloneHint: "git clone https://%s/<owner>/<repo>.git",
	}
	if v := strings.TrimSpace(os.Getenv("CODE_HOST")); v != "" {
		code.host, code.overridden = v, true
	}
	return []gitTarget{code}
}

// targetFor returns the row git's attribute block names, if any. Production
// requires https; a host override (dev deployments) may be plain http.
func targetFor(attrs map[string]string) (gitTarget, bool) {
	for _, t := range gitTargets() {
		if !strings.EqualFold(attrs["host"], t.host) {
			continue
		}
		switch attrs["protocol"] {
		case "https":
			return t, true
		case "http":
			return t, t.overridden
		}
	}
	return gitTarget{}, false
}

// newGitCredentialCmd is the git credential helper for Latere Code. git
// invokes it as `latere git-credential get|store|erase` with an attribute
// block on stdin, so `git clone https://code.latere.ai/<owner>/<repo>.git`
// works with no token in the URL after `latere login`.
func newGitCredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git-credential",
		Short: "Git credential helper for Latere Code (code.latere.ai).",
		Long: `Authenticate git against Latere Code (code.latere.ai) with the login
saved by 'latere login'.

git invokes this helper as 'latere git-credential get|store|erase',
writing an attribute block (protocol, host, ...) to stdin. 'get' answers
only for that host: it refreshes the saved login when expired, mints a
5-minute token bound to the host's audience from it, and emits that token
as username/password lines. The login token itself never reaches git.
'store' and 'erase' are no-ops: the login lives in ~/.config/latere,
managed by 'latere login' and 'latere logout', never in git's own store.

Run 'latere git-credential setup' once to wire the helper into your
global git config, scoped to that host only.`,
		Example: `  latere login
  latere git-credential setup
  git clone https://code.latere.ai/<owner>/<repo>.git`,
	}
	cmd.AddCommand(newGitCredentialGetCmd())
	cmd.AddCommand(newGitCredentialNoopCmd("store"))
	cmd.AddCommand(newGitCredentialNoopCmd("erase"))
	cmd.AddCommand(newGitCredentialSetupCmd())
	return cmd
}

// newGitCredentialSetupCmd wires the helper into the user's global git
// config, scoped to the hosts in gitTargets. Each scheme gets two entries:
// an empty helper first, which makes git discard credential helpers
// inherited from broader config scopes (e.g. osxkeychain from the system
// gitconfig) for this host — so no other helper caches or serves a stale
// token — then the real helper.
func newGitCredentialSetupCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure git to use this helper for Latere Code (undo with --remove).",
		Long: `Write the global git config entries that route Latere Code credentials
through this helper:

    credential.https://<host>.helper =                        (reset)
    credential.https://<host>.helper = !latere git-credential

The empty first entry clears helpers inherited from wider git config
scopes for that host, so only this helper answers there. Helpers for
every other host are untouched. Re-running setup is idempotent; --remove
deletes the entries for each scheme. A nonblank CODE_HOST override
configures HTTP as well as HTTPS for that development host.`,
		Example: `  latere git-credential setup
  latere git-credential setup --remove`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			keys := gitHelperKeys()
			errw := cmd.ErrOrStderr()
			if remove {
				for _, key := range keys {
					if err := gitConfigUnsetAll(cmd.Context(), key); err != nil {
						return err
					}
					fprintf(errw, "Removed %s from the global git config.\n", key)
				}
				return nil
			}
			if err := writeGitHelperConfig(cmd.Context()); err != nil {
				return err
			}
			fprintf(errw, "Configured the global git config:\n")
			for _, key := range keys {
				fprintf(errw, "  %s=                          (resets inherited helpers)\n", key)
				fprintf(errw, "  %s=!latere git-credential\n\n", key)
			}
			for _, t := range gitTargets() {
				fprintf(errw, "Git now authenticates to %s with short-lived tokens minted from `latere login`.\n", t.host)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the Latere Code credential-helper entries from the global git config")
	return cmd
}

// gitHelperKeys is every git config key setup writes, across every row of
// gitTargets.
func gitHelperKeys() []string {
	var keys []string
	for _, t := range gitTargets() {
		keys = append(keys, fmt.Sprintf("credential.https://%s.helper", t.host))
		if t.overridden {
			keys = append(keys, fmt.Sprintf("credential.http://%s.helper", t.host))
		}
	}
	return keys
}

// writeGitHelperConfig writes the reset + helper entries for every git host
// into the global git config. --replace-all collapses any previous
// entries into the single empty reset entry, making re-runs idempotent;
// --add appends the real helper after it.
func writeGitHelperConfig(ctx context.Context) error {
	for _, key := range gitHelperKeys() {
		if err := gitConfig(ctx, "--replace-all", key, ""); err != nil {
			return err
		}
		if err := gitConfig(ctx, "--add", key, "!latere git-credential"); err != nil {
			return err
		}
	}
	return nil
}

// gitHelperConfigured reports whether the global git config already
// carries the reset + helper pair for every scheme setup configures.
func gitHelperConfigured(ctx context.Context) bool {
	for _, key := range gitHelperKeys() {
		out, err := exec.CommandContext(ctx, "git", "config", "--global", "--get-all", key).Output()
		if err != nil || string(out) != "\n!latere git-credential\n" {
			return false
		}
	}
	return true
}

// configureGitAfterLogin is the post-login hook `latere login` runs
// (unless --no-git). Swappable for tests.
var configureGitAfterLogin = autoConfigureGit

// autoConfigureGit wires the git credential helper after a successful
// login. Best-effort by design — login must never fail over git
// config: no git binary on PATH is a silent skip, and a git config error
// degrades to one quiet warning pointing at the manual command. Skips the
// write when the entries are already in place.
func autoConfigureGit(ctx context.Context, errw io.Writer) {
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	if !gitHelperConfigured(ctx) {
		if err := writeGitHelperConfig(ctx); err != nil {
			fprintf(errw, "  warning: could not configure git (%v); run `latere git-credential setup` manually\n", err)
			return
		}
	}
	for _, t := range gitTargets() {
		fprintf(errw, "git is configured for %s (clone with "+t.cloneHint+")\n", t.host, t.host)
	}
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
		Short: "Emit a token from the saved Latere login for a Latere Code git request (called by git).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, ok := credentialRequestTarget(cmd.InOrStdin())
			if !ok {
				return nil
			}
			access, err := gitCredentialToken(cmd.Context(), authURL, target)
			if err != nil {
				return nil //nolint:nilerr // a helper miss is silence by protocol: git then prompts
			}
			// Git values must fit one NUL-free line. Decline malformed tokens
			// rather than letting their bytes become credential attributes.
			if strings.ContainsAny(access, "\r\n\x00") {
				return nil
			}
			// Origo reads the Basic password as the bearer token and ignores
			// the username; the row carries its convention.
			fprintf(cmd.OutOrStdout(), "username=%s\npassword=%s\n\n", target.username, access)
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

// codeAudience is the aud claim Origo enforces on every token it accepts:
// the literal "origo", not the hostname. See origo's
// internal/auth.AudienceOrigo.
const codeAudience = "origo"

// gitCredentialToken is the bearer presented to a git host: a token minted
// for that host's audience alone from the saved login.
func gitCredentialToken(ctx context.Context, authURL string, target gitTarget) (string, error) {
	bearer, _, err := api.ActorToken(ctx, api.ResolveAuthURL("", authURL), target.audience)
	if err != nil {
		return "", fmt.Errorf("cannot authenticate to %s: %w", target.name, err)
	}
	return bearer, nil
}

// parseCredentialAttrs reads git's credential-helper attribute block: one
// `key=value` per line, terminated by a blank line or EOF. Values may
// contain `=`; lines without one are ignored, matching git's tolerance.
// credentialRequestTarget reports which git host, if any, git is asking for
// a credential for. Every miss reads the same -- an unreadable request as
// much as an unknown host -- because a credential helper must never break
// `git fetch`: the command then prints nothing, exits 0, and git prompts.
func credentialRequestTarget(r io.Reader) (gitTarget, bool) {
	attrs, err := parseCredentialAttrs(r)
	if err != nil {
		return gitTarget{}, false
	}
	return targetFor(attrs)
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
