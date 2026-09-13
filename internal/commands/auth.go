// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"latere.ai/x/pkg/otel"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"latere.ai/x/pkg/authkit/cli"
	"latere.ai/x/pkg/authkit/oidc"

	"github.com/latere-ai/latere-cli/internal/api"
)

// newAuthCmd is a hidden back-compat alias: the session verbs live at the
// top level (latere login/whoami/print-token/logout/org). Children are built
// from the same factories as the top-level verbs so behavior cannot drift.
// Scripts written against `latere auth <verb>` keep working silently; remove
// the alias in a later major version.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "auth",
		Short:  "Deprecated alias for the top-level session commands.",
		Hidden: true,
	}
	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthWhoamiCmd())
	cmd.AddCommand(newAuthPrintTokenCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	cmd.AddCommand(newAuthOrgCmd())
	return cmd
}

// newOrgCmd is the top-level organization-context verb. With no argument it
// prints the active context of the saved token; with an org UUID (or
// --personal) it switches the context via the refresh-token grant, so the
// user does not need to re-run device-code login.
func newOrgCmd() *cobra.Command {
	var authURL, clientID string
	var personal bool
	cmd := &cobra.Command{
		Use:   "org [org-uuid]",
		Short: "Show or switch the active organization context.",
		Long: `Show or switch which organization the saved token is scoped to.

    latere org                # show the active context
    latere org <org-uuid>     # switch to <org-uuid>
    latere org --personal     # switch to the personal context

Switch uses the refresh-token grant; the user does not need to
re-run device-code login.`,
		Example: `  latere org
  latere org 3fa85f64-5717-4562-b3fc-2c963f66afa6
  latere org --personal`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if personal && len(args) == 1 {
				return errors.New("--personal and an org id are mutually exclusive")
			}
			if !personal && len(args) == 0 {
				return showOrgContext(cmd)
			}
			orgID := ""
			if len(args) == 1 {
				orgID = args[0]
			}
			return switchOrgContext(cmd, authURL, clientID, orgID)
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "auth service base URL (default $AUTH_URL or https://auth.latere.ai)")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client id (default saved login client, then $AUTH_CLIENT_ID or latere-cli)")
	cmd.Flags().BoolVar(&personal, "personal", false, "switch to the personal context")
	return cmd
}

// newAuthOrgCmd keeps `latere auth org switch` working under the hidden
// alias. It shares switchOrgContext with the top-level org verb.
func newAuthOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "org",
		Short:  "Deprecated alias for `latere org`.",
		Hidden: true,
	}
	cmd.AddCommand(newAuthOrgSwitchCmd())
	return cmd
}

func newAuthOrgSwitchCmd() *cobra.Command {
	var authURL, clientID string
	var personal bool
	cmd := &cobra.Command{
		Use:   "switch <org-uuid>",
		Short: "Switch the active org context using the saved refresh token.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if personal && len(args) == 1 {
				return errors.New("--personal and an org id are mutually exclusive")
			}
			orgID := ""
			if len(args) == 1 {
				orgID = args[0]
			}
			if personal {
				orgID = ""
			}
			return switchOrgContext(cmd, authURL, clientID, orgID)
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "auth service base URL (default $AUTH_URL or https://auth.latere.ai)")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client id (default saved login client, then $AUTH_CLIENT_ID or latere-cli)")
	cmd.Flags().BoolVar(&personal, "personal", false, "switch to the personal context (equivalent to `switch \"\"`)")
	return cmd
}

// showOrgContext prints the active context without a network call: the org
// scope is stamped into the JWT claims at issue time. Prints "personal" or
// the org UUID, bare, so it is scriptable.
func showOrgContext(cmd *cobra.Command) error {
	tok, err := api.LoadAuthToken()
	if err != nil {
		return err
	}
	info, err := principalFromJWT(tok.AccessToken)
	if err != nil {
		return err
	}
	if info.OrgID == "" {
		fprintln(cmd.OutOrStdout(), "personal")
	} else {
		fprintln(cmd.OutOrStdout(), info.OrgID)
	}
	return nil
}

// switchOrgContext re-scopes the saved token to orgID (empty = personal)
// using the refresh-token grant with `org_id=<uuid>`.
func switchOrgContext(cmd *cobra.Command, authURL, clientID, orgID string) error {
	tok, err := api.LoadAuthToken()
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("no refresh token on file; run `latere login` first")
	}

	authBase := authURL
	if authBase == "" {
		authBase = os.Getenv("AUTH_URL")
		if authBase == "" {
			authBase = "https://auth.latere.ai"
		}
	}
	authBase = strings.TrimRight(authBase, "/")
	if clientID == "" {
		clientID = tok.ClientID
	}
	cid := api.AuthClientID(clientID)

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {cid},
		"org_id":        {orgID},
	}
	req, err := http.NewRequestWithContext(cmd.Context(),
		http.MethodPost,
		authBase+"/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token endpoint: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if readErr != nil {
		return fmt.Errorf("read token response: %w", readErr)
	}
	var got struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("decode token: %w", err)
	}
	if got.AccessToken == "" {
		return errors.New("token endpoint returned no access_token")
	}

	// Match OAuth refresh handling: an absent or zero lifetime means the
	// expiry is unknown, rather than that the new token expired immediately.
	var expiry time.Time
	if got.ExpiresIn != 0 {
		expiry = time.Now().Add(time.Duration(got.ExpiresIn) * time.Second).UTC()
	}
	if got.RefreshToken == "" {
		got.RefreshToken = tok.RefreshToken
	}
	if err := api.SaveAuthToken(api.Token{
		AccessToken:  got.AccessToken,
		RefreshToken: got.RefreshToken,
		ClientID:     cid,
		TokenType:    "Bearer",
		ExpiresAt:    expiry,
		IssuedAt:     time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("save login token: %w", err)
	}
	if orgID == "" {
		fprintln(cmd.ErrOrStderr(), "Switched to personal context.")
	} else {
		fprintf(cmd.ErrOrStderr(), "Switched to org %s.\n", orgID)
	}
	return nil
}

// newAuthPrintTokenCmd prints the saved login token to stdout so it
// can be embedded in shell scripts: `TOKEN=$(latere print-token)`.
// Writes one trailing newline, which shell command substitution removes.
func newAuthPrintTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print-token",
		Args:  cobra.NoArgs,
		Short: "Print the saved login token to stdout (for use in scripts).",
		Long: `Print the access token from ~/.config/latere/auth-token.json.

The token is addressed to auth.latere.ai and opens nothing else: a
product refuses it. To reach a product, ask for a token minted for that
product, e.g. 'latere lux env --raw' for Lux.

    TOKEN=$(latere print-token)
    curl -H "Authorization: Bearer $TOKEN" https://auth.latere.ai/tokeninfo`,
		Example: `  TOKEN=$(latere print-token)
  curl -H "Authorization: Bearer $TOKEN" https://auth.latere.ai/tokeninfo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, err := api.LoadAuthToken()
			if err != nil {
				return err
			}
			if tok.AccessToken == "" {
				return api.ErrNoToken
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), tok.AccessToken)
			return err
		},
	}
}

func newAuthLoginCmd() *cobra.Command {
	var (
		token     string
		authURL   string
		clientID  string
		scopes    string
		personal  bool
		orgID     string
		noBrowser bool
		noGit     bool
	)
	cmd := &cobra.Command{
		Use:   "login",
		Args:  cobra.NoArgs,
		Short: "Sign in via OAuth2 device-code (or paste a token with --token).",
		Long: `Sign in to Latere.

By default, login starts the OAuth2 device-code flow against
auth.latere.ai: it prints a short user code and a URL, you visit the
URL in any browser to approve, choose the Personal or Organization
context for the token, and the CLI then polls until the approval lands.
The resulting access token is written to
~/.config/latere/auth-token.json with 0600 perms. It is addressed to
auth.latere.ai alone; every product call presents a five-minute token
minted from it for that one product.

Use --personal or --org-id to preselect the token context from the
terminal. Re-run login with a different context to switch which cellas
the CLI can list and operate.

After a successful login the CLI also wires git's credential helper for
code.latere.ai (idempotent, scoped to that host only), so plain
'git clone https://code.latere.ai/<owner>/<repo>.git' works with no
token in the URL. Pass --no-git to leave your git config untouched;
'latere git-credential setup --remove' undoes the wiring later.

For unattended setups (CI, scripts), pass --token to skip the device
flow and store an access token directly. A pasted token keeps its existing
context; --personal and --org-id apply only to browser login.`,
		Example: `  latere login
  latere login --personal
  latere login --org-id org_123
  latere login --no-browser
  latere login --no-git
  latere login --token "$LATERE_TOKEN"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if personal && strings.TrimSpace(orgID) != "" {
				return errors.New("--personal and --org-id are mutually exclusive")
			}

			// Token-paste fast path: --token wins, or stdin pipe falls
			// back to it. The device flow only kicks in for an
			// interactive terminal with no --token.
			pasteLogin := func(t string) error {
				if personal || strings.TrimSpace(orgID) != "" {
					return errors.New("--personal and --org-id cannot change a pasted token's context; omit these flags or sign in through the browser")
				}
				return loginWithPastedToken(ctx, authURL, t)
			}
			login := func() error {
				if t := strings.TrimSpace(token); t != "" {
					return pasteLogin(t)
				}
				stat, err := os.Stdin.Stat()
				if err != nil {
					return fmt.Errorf("inspect stdin: %w", err)
				}
				if (stat.Mode() & os.ModeCharDevice) == 0 {
					b, err := readAll(os.Stdin)
					if err != nil {
						return err
					}
					if t := strings.TrimSpace(b); t != "" {
						return pasteLogin(t)
					}
				}
				return runDeviceFlow(ctx, deviceFlowOpts{
					AuthURL:   authURL,
					ClientID:  clientID,
					Scopes:    scopes,
					OrgID:     strings.TrimSpace(orgID),
					OrgIDSet:  personal || strings.TrimSpace(orgID) != "",
					NoBrowser: noBrowser,
				})
			}
			if err := login(); err != nil {
				return err
			}
			// Every login variant ends by wiring git for Latere Code
			// (best-effort, never fatal) so `git clone https://code.latere.ai/...`
			// is a one-step story after sign-in.
			if !noGit {
				configureGitAfterLogin(ctx, cmd.ErrOrStderr())
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&token, "token", "", "skip device flow; store an access token directly")
	f.StringVar(&authURL, "auth-url", "", "override auth base URL (default $AUTH_URL or https://auth.latere.ai)")
	f.StringVar(&clientID, "client-id", "latere-cli", "OAuth client_id used for the device-code request")
	f.StringVar(&scopes, "scopes", api.LoginScopes,
		"space-delimited scope list")
	f.BoolVar(&personal, "personal", false, "issue the CLI token for personal cellas")
	f.StringVar(&orgID, "org-id", "", "issue the CLI token for this organization id")
	f.BoolVar(&noBrowser, "no-browser", false, "print the device URL without opening a browser")
	f.BoolVar(&noGit, "no-git", false, "do not configure git's credential helper for code.latere.ai")
	return cmd
}

func loginWithPastedToken(ctx context.Context, authURL, token string) error {
	authBase := api.ResolveAuthURL("", authURL)
	if err := verifyAtIssuer(ctx, authBase, token); err != nil {
		return err
	}
	// A pasted token has no refresh grant, so nothing of a previous login
	// survives beside it: the file holds one credential and this is it.
	if err := api.SaveAuthToken(api.Token{
		AccessToken: token,
		TokenType:   "Bearer",
		IssuedAt:    time.Now().UTC(),
	}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in. Token saved to %s\n", api.AuthTokenPath())
	return nil
}

// verifyAtIssuer confirms a pasted token at the issuer before it is
// stored. The login token is addressed to auth, so auth is who can say
// whether it is a login at all; storing an unverified string would fail
// later at every product with an error naming the wrong service.
func verifyAtIssuer(ctx context.Context, authBase, token string) error {
	c := api.NewClient(authBase)
	c.SetBearer(token, time.Time{})
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ignored any
	if err := c.GetJSON(verifyCtx, "/tokeninfo", &ignored); err != nil {
		return fmt.Errorf("token rejected by auth: %w", err)
	}
	return nil
}

// ---- device-code flow ----

type deviceFlowOpts struct {
	AuthURL, ClientID, Scopes string
	OrgID                     string
	OrgIDSet                  bool
	NoBrowser                 bool
}

// captureStore holds the device-flow candidate in memory until the login
// is complete. A rejected login must not replace the credential the
// commands already work with.
type captureStore struct {
	disk     *cli.FileTokenStore
	last     *oauth2.Token
	clientID string
}

func newAuthTokenStore(clientID string) (*captureStore, error) {
	p := api.AuthTokenPath()
	if p == "" {
		return nil, errors.New("cannot determine auth token path")
	}
	disk, err := cli.NewFileTokenStore(p)
	if err != nil {
		return nil, err
	}
	return &captureStore{disk: disk, clientID: clientID}, nil
}

func (s *captureStore) Save(t *oauth2.Token) error {
	if t == nil {
		return errors.New("nil token")
	}
	s.last = t
	return nil
}

// persist writes the approved login: the one credential on disk, from
// which every product token is minted.
func (s *captureStore) persist() error {
	t := s.last
	if err := api.SaveAuthToken(api.Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ClientID:     s.clientID,
		TokenType:    "Bearer",
		ExpiresAt:    t.Expiry,
		IssuedAt:     time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("save login token: %w", err)
	}
	return nil
}

func (s *captureStore) Load() (*oauth2.Token, error) { return s.disk.Load() }
func (s *captureStore) Clear() error                 { return s.disk.Clear() }

// runDeviceFlow drives the RFC 8628 device-code flow against
// auth.latere.ai via pkg/cli.DeviceCodeClient and saves the approved
// token. Auth issues it for auth alone; nothing is traded anywhere else.
func runDeviceFlow(ctx context.Context, opts deviceFlowOpts) error {
	opts.AuthURL = api.ResolveAuthURL("", opts.AuthURL)

	client := oidc.New(oidc.Config{
		AuthURL:  opts.AuthURL,
		ClientID: opts.ClientID,
		Scopes:   strings.Fields(opts.Scopes),
	})
	if client == nil {
		return errors.New("oidc: missing AuthURL or ClientID")
	}

	store, err := newAuthTokenStore(opts.ClientID)
	if err != nil {
		return err
	}

	extra := url.Values{}
	if opts.OrgIDSet {
		// Forward the present-but-possibly-empty value. Auth reads
		// `?org_id=` (explicit empty) as "personal context" — silently
		// dropping it would turn --personal into a no-op.
		extra["org_id"] = []string{opts.OrgID}
	}

	dcc := cli.NewDeviceCodeClient(client, store)
	dcc.Output = os.Stderr
	dcc.ExtraParams = extra
	if opts.NoBrowser {
		dcc.OpenBrowser = func(string) error { return nil }
	} else {
		dcc.OpenBrowser = openBrowser
	}

	if err := dcc.Login(ctx); err != nil {
		// Surface terminal RFC 8628 errors with the CLI's user-facing
		// strings; everything else passes through with authkit's wrap.
		if rerr, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
			switch rerr.ErrorCode {
			case "expired_token":
				return errors.New("device code expired before approval")
			case "access_denied":
				return errors.New("user denied the request")
			}
			return fmt.Errorf("device-code login failed: %s (%s)", rerr.ErrorCode, rerr.ErrorDescription)
		}
		return err
	}

	tok := store.last
	if tok == nil || tok.AccessToken == "" {
		return errors.New("token endpoint returned no access_token")
	}

	if err := store.persist(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in. Token saved to %s\n", api.AuthTokenPath())
	return nil
}

var openBrowser = func(rawURL string) error {
	name, args, err := browserCommand(rawURL)
	if err != nil {
		return err
	}
	return exec.Command(name, args...).Start()
}

func browserCommand(rawURL string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{rawURL}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}, nil
	case "linux":
		return "xdg-open", []string{rawURL}, nil
	default:
		return "", nil, fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

func newAuthWhoamiCmd() *cobra.Command {
	var authURL string
	cmd := &cobra.Command{
		Use:   "whoami",
		Args:  cobra.NoArgs,
		Short: "Print the current principal.",
		Long: `Print the principal the saved login names.

The login token is addressed to auth.latere.ai, so auth is asked:
'latere whoami' reads ~/.config/latere/auth-token.json, refreshes it if
it is due, and calls auth's token-introspection endpoint. If auth
cannot be reached, the identity claims carried in the saved token are
printed instead.`,
		Example: `  latere whoami
  latere whoami --auth-url https://auth.latere.ai`,
		RunE: func(cmd *cobra.Command, args []string) error {
			access, authBase, err := api.LoginToken(cmd.Context(), authURL)
			if err != nil {
				return err
			}
			c := api.NewClient(authBase)
			c.SetBearer(access, time.Time{})
			var info struct {
				Sub           string   `json:"sub"`
				Email         *string  `json:"email,omitempty"`
				PrincipalType string   `json:"principal_type"`
				OrgID         *string  `json:"org_id,omitempty"`
				Scopes        []string `json:"scopes"`
				ClientID      string   `json:"client_id,omitempty"`
			}
			// A successful status with no subject (including null or 204) does
			// not identify a principal. Use the local fallback in that case.
			if err := c.GetJSON(cmd.Context(), "/tokeninfo", &info); err == nil && info.Sub != "" {
				return printPrincipal(cmd.OutOrStdout(), principalInfo{
					Sub:           info.Sub,
					Email:         deref(info.Email),
					PrincipalType: info.PrincipalType,
					OrgID:         deref(info.OrgID),
					Scopes:        info.Scopes,
					ClientID:      info.ClientID,
				})
			}
			// Introspection is best-effort: the inferred auth host may not
			// resolve on a custom deployment. The saved token carries the
			// same identity claims, so read them locally.
			local, err := principalFromJWT(access)
			if err != nil {
				return err
			}
			return printPrincipal(cmd.OutOrStdout(), local)
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "override auth base URL (default $AUTH_URL or https://auth.latere.ai)")
	return cmd
}

type principalInfo struct {
	Sub           string
	Email         string
	PrincipalType string
	OrgID         string
	Scopes        []string
	ClientID      string
}

func printPrincipal(dst io.Writer, info principalInfo) error {
	var out strings.Builder
	fmt.Fprintf(&out, "sub:           %s\n", info.Sub)
	if info.Email != "" {
		fmt.Fprintf(&out, "email:         %s\n", info.Email)
	}
	fmt.Fprintf(&out, "principal:     %s\n", info.PrincipalType)
	if info.OrgID != "" {
		out.WriteString("context:       org\n")
		fmt.Fprintf(&out, "org_id:        %s\n", info.OrgID)
	} else {
		out.WriteString("context:       personal\n")
	}
	if info.ClientID != "" {
		fmt.Fprintf(&out, "client_id:     %s\n", info.ClientID)
	}
	if len(info.Scopes) > 0 {
		fmt.Fprintf(&out, "scopes:        %s\n", strings.Join(info.Scopes, " "))
	}
	if _, err := fmt.Fprint(dst, out.String()); err != nil {
		return fmt.Errorf("write principal: %w", err)
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func principalFromJWT(raw string) (principalInfo, error) {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return principalInfo{}, errors.New("saved token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return principalInfo{}, fmt.Errorf("decode token payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return principalInfo{}, fmt.Errorf("parse token payload: %w", err)
	}
	info := principalInfo{
		Sub:           stringClaim(claims, "sub"),
		Email:         stringClaim(claims, "email"),
		PrincipalType: stringClaim(claims, "principal_type"),
		OrgID:         stringClaim(claims, "org_id"),
		Scopes:        scopesClaim(claims),
		ClientID:      stringClaim(claims, "client_id"),
	}
	if info.Sub == "" {
		return principalInfo{}, errors.New("saved token is missing sub")
	}
	if info.PrincipalType == "" {
		info.PrincipalType = "user"
	}
	return info, nil
}

func stringClaim(claims map[string]any, key string) string {
	v, _ := claims[key].(string)
	return v
}

func scopesClaim(claims map[string]any) []string {
	if scope, _ := claims["scope"].(string); scope != "" {
		return strings.Fields(scope)
	}
	raw, ok := claims["scp"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		return strings.Fields(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func newAuthLogoutCmd() *cobra.Command {
	var authURL string
	cmd := &cobra.Command{
		Use:   "logout",
		Args:  cobra.NoArgs,
		Short: "Sign out: revoke the session server-side and clear the saved login.",
		Long: `Sign out of Latere.

Revokes the saved refresh token at auth (RFC 7009 /revoke), then
deletes ~/.config/latere/auth-token.json. Revocation is best-effort: an
unreachable or older server prints a warning and the local sign-out
still completes. Tokens already minted for a product are not recalled;
each lapses within five minutes.`,
		Example: `  latere logout
  latere login`,
		RunE: func(cmd *cobra.Command, args []string) error {
			revokeAuthRefreshToken(cmd.Context(), authURL, cmd.ErrOrStderr())
			if err := api.ClearAuthToken(); err != nil {
				return err
			}
			fprintln(cmd.ErrOrStderr(), "Logged out.")
			return nil
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "override auth base URL (default $AUTH_URL or https://auth.latere.ai)")
	return cmd
}

// revokeAuthRefreshToken best-effort revokes the saved refresh token via
// RFC 7009 (POST {auth}/revoke, public client) so the login cannot mint
// further access tokens after sign-out.
func revokeAuthRefreshToken(ctx context.Context, authURL string, errw io.Writer) {
	tok, err := api.LoadAuthToken()
	if err != nil || tok.RefreshToken == "" {
		return
	}
	authBase := api.ResolveAuthURL("", authURL)
	cid := api.AuthClientID(tok.ClientID)
	form := url.Values{
		"token":           {tok.RefreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {cid},
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, authBase+"/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		fprintf(errw, "  warning: could not revoke the refresh token (%v)\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 10 * time.Second, Transport: otel.Transport(nil), CheckRedirect: api.PreserveMethodOnRedirect}).Do(req)
	if err != nil {
		fprintf(errw, "  warning: could not revoke the refresh token (%v); it remains valid until expiry\n", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, readErr := io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		fprintf(errw, "  warning: refresh-token revocation returned %d; it may remain valid until expiry\n", resp.StatusCode)
	} else if readErr != nil {
		fprintf(errw, "  warning: could not confirm refresh-token revocation (%v); it may remain valid until expiry\n", readErr)
	}
}

// readAll reads all of r into a string. Bounded at 64KiB to keep a
// noisy stdin from filling memory.
func readAll(r interface {
	Read([]byte) (int, error)
}) (string, error) {
	const max = 64 << 10
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > max {
				return "", errors.New("token input too large")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	return string(buf), nil
}
