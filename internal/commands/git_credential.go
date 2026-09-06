// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"latere.ai/x/pkg/otel"

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
only for the Drive host: it refreshes the saved login when expired, mints
a 5-minute token bound to Drive's audience from it, and emits that token
as username/password lines. The login token itself never reaches git.
'store' and 'erase' are no-ops: the login lives in ~/.config/latere,
managed by 'latere login' and 'latere logout', never in git's own store.

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
// config, scoped to the Drive host only. Each scheme gets two entries: an empty
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
--remove deletes the entries for each scheme. A nonblank DRIVE_HOST
override configures HTTP as well as HTTPS for that development host.`,
		Example: `  latere git-credential setup
  latere git-credential setup --remove`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			keys := driveGitHelperKeys()
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
			if err := writeDriveGitHelperConfig(cmd.Context()); err != nil {
				return err
			}
			fprintf(errw, "Configured the global git config:\n")
			for _, key := range keys {
				fprintf(errw, "  %s=                          (resets inherited helpers)\n", key)
				fprintf(errw, "  %s=!latere git-credential\n\n", key)
			}
			fprintf(errw, "Git now authenticates to %s with short-lived tokens minted from `latere login`.\n", driveHost())
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the Drive credential-helper entries from the global git config")
	return cmd
}

func driveGitHelperKeys() []string {
	host := driveHost()
	keys := []string{fmt.Sprintf("credential.https://%s.helper", host)}
	if strings.TrimSpace(os.Getenv("DRIVE_HOST")) != "" {
		keys = append(keys, fmt.Sprintf("credential.http://%s.helper", host))
	}
	return keys
}

// writeDriveGitHelperConfig writes the reset + helper entries for the Drive
// host into the global git config. --replace-all collapses any previous
// entries into the single empty reset entry, making re-runs idempotent;
// --add appends the real helper after it.
func writeDriveGitHelperConfig(ctx context.Context) error {
	for _, key := range driveGitHelperKeys() {
		if err := gitConfig(ctx, "--replace-all", key, ""); err != nil {
			return err
		}
		if err := gitConfig(ctx, "--add", key, "!latere git-credential"); err != nil {
			return err
		}
	}
	return nil
}

// driveGitHelperConfigured reports whether the global git config already
// carries the reset + helper pair for every scheme setup configures.
func driveGitHelperConfigured(ctx context.Context) bool {
	for _, key := range driveGitHelperKeys() {
		out, err := exec.CommandContext(ctx, "git", "config", "--global", "--get-all", key).Output()
		if err != nil || string(out) != "\n!latere git-credential\n" {
			return false
		}
	}
	return true
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
			access, err := driveCredentialToken(cmd.Context(), authURL)
			if err != nil {
				return nil //nolint:nilerr // a helper miss is silence by protocol: git then prompts
			}
			// Git values must fit one NUL-free line. Decline malformed tokens
			// rather than letting their bytes become credential attributes.
			if strings.ContainsAny(access, "\r\n\x00") {
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
	switch attrs["protocol"] {
	case "https":
		return true
	case "http":
		return strings.TrimSpace(os.Getenv("DRIVE_HOST")) != ""
	default:
		return false
	}
}

// driveAudience is the aud claim Drive enforces on the bearer git presents.
// It is the production audience regardless of DRIVE_HOST: the override
// selects which git host the helper answers for, not which audience auth
// stamps.
const driveAudience = "drive.latere.ai"

// driveActorTTL bounds the token git receives, in seconds. A git exchange
// completes in seconds, so five minutes covers it and limits the window of
// a value that leaks through git's own credential store or a trace.
const driveActorTTL = 300

// driveCredentialToken resolves the bearer presented to Drive, by the git
// helper and the `latere drive` file commands alike: a short-lived actor
// token bound to driveAudience, minted at auth with the retained root token
// (refreshed when expired via the same authIdentityToken path `latere lux`
// uses). The root token itself is never presented: its audience is auth,
// sandboxd and toposd, and Drive rejects it. Falls back to token.json only
// when the auth file is absent, as it is after --token paste login; that
// case returns api.ErrNoToken when token.json is empty too. Existing auth
// failures must not change identity, so they are returned, not masked by
// the fallback.
func driveCredentialToken(ctx context.Context, authURL string) (string, error) {
	access, authBase, err := authIdentityToken(ctx, "", authURL)
	if err == nil {
		return mintDriveActorToken(ctx, authBase, access)
	}
	if !errors.Is(err, api.ErrNoToken) {
		return "", err
	}
	if tok, lerr := api.LoadToken(""); lerr == nil && tok.AccessToken != "" {
		return tok.AccessToken, nil
	}
	return "", err
}

// mintDriveActorToken exchanges the root token for a driveAudience actor
// token. Any failure, auth unreachable included, is an error the caller
// decides how to surface: the git helper stays silent so git prompts, the
// file commands report it.
func mintDriveActorToken(ctx context.Context, authBase, access string) (string, error) {
	httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil)}
	actor, err := api.MintActorToken(ctx, httpc, authBase, access, driveAudience, driveActorTTL)
	if err != nil {
		return "", fmt.Errorf("mint Drive token: %w; if this persists run `latere login`", err)
	}
	return actor, nil
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
